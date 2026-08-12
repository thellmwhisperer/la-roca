package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// Database is the little of the store the ingest needs. It travels as an interface
// so this package can be exercised without one, which is what keeps the parser
// suite free of any database at all.
type Database interface {
	SQL() *sql.DB
	Write(ctx context.Context, fn func(*sql.Tx) error) error
}

// Options are one run's inputs.
type Options struct {
	Roots Roots
	// DryRun reports what would be read and writes nothing. It is a first-class
	// mode and not a debugging aid: it is how an operator checks that a root is
	// being seen before letting anything touch the database.
	DryRun bool
	// Progress receives terse source-level lines while a human-facing command
	// waits. JSON and MCP callers leave it nil.
	Progress func(string)
	// LiveProgress carries counters for an interactive renderer. It is separate
	// from Progress so plain streams keep their stable sequential lines.
	LiveProgress func(SourceProgress)
}

// SourceProgress is one redraw of a source row. Processed counts artefacts
// inspected, including unchanged fingerprints; Read counts artefacts parsed.
type SourceProgress struct {
	Source    string
	Processed int
	Total     int
	Read      int
	Sessions  int
	Discarded int
	ElapsedMS int64
	Done      bool
}

// SourceStats attributes work that used to disappear inside the ingest total.
// It is presentation data, not part of the JSON contract.
type SourceStats struct {
	Processed        int
	Read             int
	RecordsDiscarded int
	RecordsExcluded  int
	ElapsedMS        int64
}

// Failure is one artefact that could not be read, named so the operator knows
// which one and why. One bad file is isolated and reported; it never costs the
// run.
type Failure struct {
	Path   string `json:"path"`
	Parser string `json:"parser"`
	Reason string `json:"error"`
}

type DiscardDetail struct {
	Path     string `json:"path"`
	Parser   string `json:"parser"`
	Record   int    `json:"record"`
	Reason   string `json:"reason"`
	ByDesign bool   `json:"by_design"`
}

// DiscardCategory is one reason with how many records met it. The collapsed
// shape is the one an operator reads: a healthy ingest of a large corpus leaves
// hundreds of thousands of runtime records unread by design, and listing them
// one by one turns a healthy run into a wall of alarm.
type DiscardCategory struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	// ByDesign separates what this build never meant to read from what it could
	// not read. Only the second is a problem anybody has to look at.
	ByDesign bool `json:"by_design"`
}

// discardDetailBudget caps the per-record list. The counts are always exact;
// what is bounded is the evidence, because a report that carries one entry per
// source record stops being a report.
const discardDetailBudget = 100

const (
	anchorConflictReason        = "exchange anchor conflict"
	thinkingWithoutNumberReason = "thinking block has no exchange number"
)

// Tables are the row counts of the five tables the ingest writes. Before, after,
// and the difference: on a second pass over the same disk the difference is zero
// in every one of them, and that is the contract, not an aspiration.
type Tables struct {
	Memories       int `json:"memories"`
	Sessions       int `json:"sessions"`
	Exchanges      int `json:"exchanges"`
	ThinkingBlocks int `json:"thinking_blocks"`
	ToolUses       int `json:"tool_uses"`
}

func (t Tables) minus(other Tables) Tables {
	return Tables{
		Memories:       t.Memories - other.Memories,
		Sessions:       t.Sessions - other.Sessions,
		Exchanges:      t.Exchanges - other.Exchanges,
		ThinkingBlocks: t.ThinkingBlocks - other.ThinkingBlocks,
		ToolUses:       t.ToolUses - other.ToolUses,
	}
}

// Workspace is the set of roots used solely to resolve session project identity.
type Workspace struct {
	Selected []string `json:"selected"`
}

// orEmpty keeps list-shaped report fields as `[]`, never `null`.
func orEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// Result is what one run did, and it is the whole of what `--json` prints.
type Result struct {
	DryRun bool `json:"dry_run"`
	// Errors is a count and not a list, first, because that is what a script
	// checks. The list is beside it.
	Errors       int       `json:"errors"`
	ErrorDetails []Failure `json:"error_details,omitempty"`
	// RecordsDiscarded counts what could not be read; RecordsExcluded counts what
	// this build never meant to read. They are apart because collapsing them is
	// what made a healthy ingest report thousands of failures.
	RecordsDiscarded int               `json:"records_discarded"`
	RecordsExcluded  int               `json:"records_excluded"`
	DiscardDetails   []DiscardDetail   `json:"discard_details,omitempty"`
	DiscardSummary   []DiscardCategory `json:"discard_summary,omitempty"`
	// Scanned is what the roots hold, per source, always with every source
	// present: a source missing from the report reads as one nobody looked at.
	Scanned map[string]int `json:"scanned"`
	// Sources is what each source wrote: a count for
	// every seeded source".
	Sources map[string]*Counts `json:"sources"`
	// Files says how the run divided its work: what it read, what it skipped by
	// fingerprint, and what a source is still writing.
	FilesRead     int `json:"files_read"`
	FilesSkipped  int `json:"files_skipped"`
	FilesExcluded int `json:"files_excluded"`
	ExchangesHeld int `json:"exchanges_deferred"`

	Before Tables `json:"counts_before"`
	After  Tables `json:"counts_after"`
	Delta  Tables `json:"delta"`

	WorkspaceRoots Workspace               `json:"workspace_roots"`
	DetectedAgents []string                `json:"detected_agents"`
	MissingAgents  []string                `json:"agents_not_found"`
	Roots          map[string]string       `json:"roots"`
	Warnings       []string                `json:"warnings,omitempty"`
	ElapsedMS      int64                   `json:"elapsed_ms"`
	SourceStats    map[string]*SourceStats `json:"-"`
	// categories indexes DiscardSummary while the run is collapsing into it.
	categories map[string]int `json:"-"`
}

// Run reads every source in the matrix once and writes what changed.
//
// Sources are read as memories, agent configuration, then transcripts. The order
// matters for one thing only, and
// it is the session titles: the transcript writes the session and the metadata
// file names it, so the first non-blank name wins deterministically.
func Run(ctx context.Context, db Database, layers layerResolver, opts Options) (result Result, err error) {
	start := time.Now()
	defer func() { result.ElapsedMS = time.Since(start).Milliseconds() }()
	plan := Scan(opts.Roots)

	result = Result{
		DryRun:  opts.DryRun,
		Scanned: plan.Scanned,
		Sources: map[string]*Counts{},
		WorkspaceRoots: Workspace{
			Selected: orEmpty(plan.WorkspaceRoots),
		},
		DetectedAgents: orEmpty(plan.DetectedAgents),
		MissingAgents:  MissingAgentFamilies(plan.DetectedAgents),
		Roots:          declaredRoots(opts.Roots),
		Warnings:       plan.Warnings,
		SourceStats:    map[string]*SourceStats{},
	}
	announced := map[string]bool{}
	liveStarted := map[string]bool{}
	totals := map[string]int{}
	remaining := map[string]int{}
	sourceElapsed := map[string]time.Duration{}
	for _, target := range plan.Targets {
		source := normalizedSource(target.SourceAgent)
		result.source(source)
		result.sourceStats(source)
		totals[source]++
		remaining[source]++
	}
	for _, target := range plan.Excluded {
		result.source(target.SourceAgent)
		stats := result.sourceStats(target.SourceAgent)
		stats.RecordsExcluded++
		result.FilesExcluded++
		// A file the scan refuses on purpose is not a failure to read one: it is
		// this build declining to ingest something it decided is not corpus.
		result.discard(target, []parsers.Discard{parsers.Excluded(target.ExclusionReason)})
	}

	// The state is read even on a dry run: telling an operator that eight hundred
	// files were found is not the same as telling them that two of them changed,
	// and the second is what they are asking.
	state, err := LoadState(ctx, db.SQL())
	if err != nil {
		if opts.DryRun {
			// A dry run answers over a database it may not be able to read, and it
			// answers anyway: this mode never fails on the operator.
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("the ingest state could not be read, so every file counts as "+
					"pending: %v", err))
			state = map[string]FileState{}
		} else {
			return result, err
		}
	}

	before, err := tableCounts(ctx, db.SQL())
	if err != nil {
		if !opts.DryRun {
			return result, err
		}
		// The dry run answers anyway, and it says so. Handing over five zeros with
		// nothing beside them reports "this database is empty" over a database
		// whose tables could not be read at all.
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("the row counts could not be read, so counts_before, "+
				"counts_after and delta are reported as zero: %v", err))
	}
	result.Before = before
	result.After = before

	for _, target := range plan.Targets {
		source := normalizedSource(target.SourceAgent)
		targetStart := time.Now()
		stats := result.sourceStats(source)
		if opts.LiveProgress != nil && !liveStarted[source] {
			opts.LiveProgress(SourceProgress{
				Source: source, Total: totals[source],
			})
			liveStarted[source] = true
		}
		finishTarget := func() {
			stats.Processed++
			sourceElapsed[source] += time.Since(targetStart)
			stats.ElapsedMS = sourceElapsed[source].Milliseconds()
			remaining[source]--
			if opts.LiveProgress != nil {
				counts := result.source(source)
				opts.LiveProgress(SourceProgress{
					Source: source, Processed: stats.Processed,
					Total: totals[source], Read: stats.Read,
					Sessions:  counts.Sessions + counts.SessionsUpdated,
					Discarded: stats.RecordsDiscarded, ElapsedMS: stats.ElapsedMS,
					Done: remaining[source] == 0,
				})
			}
		}
		fingerprint, err := targetFingerprint(target)
		if err != nil {
			metadata, metadataErr := metadataFingerprint(target.Path)
			isDatabase := target.Kind == parsers.KindOpenCodeDB || target.Kind == parsers.KindHermesDB
			if metadataErr == nil && !isDatabase && unchangedMetadata(state, target.Path, metadata) {
				result.FilesSkipped++
				finishTarget()
				continue
			}
			result.fingerprintFailure(target, err)
			finishTarget()
			continue
		}
		if Unchanged(state, target.Path, fingerprint) {
			result.FilesSkipped++
			finishTarget()
			continue
		}
		if opts.DryRun {
			result.FilesRead++
			stats.Read++
			finishTarget()
			continue
		}
		if opts.Progress != nil && !announced[source] {
			opts.Progress("ingest: reading " + source)
			announced[source] = true
		}
		result.FilesRead++
		stats.Read++
		discardsBefore, excludedBefore := result.RecordsDiscarded, result.RecordsExcluded
		err = ingestOne(ctx, db, layers, opts, target, fingerprint, &result)
		stats.RecordsDiscarded += result.RecordsDiscarded - discardsBefore
		stats.RecordsExcluded += result.RecordsExcluded - excludedBefore
		finishTarget()
		if err != nil {
			return result, err
		}
	}

	if !opts.DryRun {
		after, err := tableCounts(ctx, db.SQL())
		if err != nil {
			return result, err
		}
		result.After = after
		result.Delta = after.minus(before)
	}
	if opts.Progress != nil {
		for _, name := range SortedSources(result.Sources) {
			counts := result.Sources[name]
			opts.Progress(fmt.Sprintf("ingest: %s complete · sessions=%d exchanges=%d memories=%d",
				name, counts.Sessions, counts.Exchanges,
				counts.MemoriesInserted+counts.MemoriesUpdated))
		}
	}
	return result, nil
}

func (r *Result) sourceStats(agent string) *SourceStats {
	agent = normalizedSource(agent)
	if stats, ok := r.SourceStats[agent]; ok {
		return stats
	}
	stats := &SourceStats{}
	r.SourceStats[agent] = stats
	return stats
}

// source makes sure a source that was scanned appears in the report even when it
// wrote nothing.
func (r *Result) source(agent string) *Counts {
	agent = normalizedSource(agent)
	if counts, ok := r.Sources[agent]; ok {
		return counts
	}
	counts := &Counts{}
	r.Sources[agent] = counts
	return counts
}

func (r *Result) recordWritten(target Target, counts Counts) {
	r.source(target.SourceAgent).add(counts)
	conflicts := make([]parsers.Discard, counts.AnchorConflicts)
	for i := range conflicts {
		conflicts[i].Reason = anchorConflictReason
	}
	r.discard(target, conflicts)
	thinking := make([]parsers.Discard, counts.ThinkingBlocksDiscarded)
	for i := range thinking {
		thinking[i].Reason = thinkingWithoutNumberReason
	}
	r.discard(target, thinking)
}

func normalizedSource(agent string) string {
	if agent == "" {
		return "unknown"
	}
	return agent
}

func (r *Result) fail(target Target, reason string) {
	r.Errors++
	r.ErrorDetails = append(r.ErrorDetails, Failure{
		Path: target.Path, Parser: string(target.Kind), Reason: reason,
	})
}

func (r *Result) fingerprintFailure(target Target, err error) {
	if os.IsNotExist(err) {
		// The file was there when the scan ran and is not there now. That is a
		// live disk, not an error worth a red run.
		r.FilesSkipped++
		return
	}
	r.fail(target, "fingerprint: "+err.Error())
}

// foreignDiscard turns one foreign-database complaint into a counted discard.
//
// Record stays absent on purpose. A database source has no record positions, and
// the complaint's index in the complaint list is not one: handing it over made
// the report say "record 2" about a file that has no second record. The identity
// the operator needs is the session id, and the complaint already carries it.
func foreignDiscard(complaint string) parsers.Discard {
	category := complaint
	for _, source := range []string{"Hermes session ", "OpenCode session "} {
		if tail, found := strings.CutPrefix(complaint, source); found {
			if _, reason, separated := strings.Cut(tail, ": "); separated {
				category = strings.TrimSuffix(source, " ") + ": " + reason
			}
			break
		}
	}
	return parsers.Discard{Reason: complaint, Category: category}
}

func (r *Result) discard(target Target, discards []parsers.Discard) {
	for _, discard := range discards {
		r.categorize(discard)
		if discard.ByDesign {
			r.RecordsExcluded++
		} else {
			r.RecordsDiscarded++
		}
		if len(r.DiscardDetails) >= discardDetailBudget {
			continue
		}
		r.DiscardDetails = append(r.DiscardDetails, DiscardDetail{
			Path: target.Path, Parser: string(target.Kind),
			Record: discard.Record, Reason: discard.Reason, ByDesign: discard.ByDesign,
		})
	}
}

// categorize collapses one discard onto its reason. The categories keep the
// order they first appeared in, which is the order the sources were read: a
// stable order is what makes two runs of the same disk comparable.
func (r *Result) categorize(discard parsers.Discard) {
	if r.categories == nil {
		r.categories = map[string]int{}
	}
	reason := discard.Category
	if reason == "" {
		reason = discard.Reason
	}
	key := fmt.Sprintf("%t\x00%s", discard.ByDesign, reason)
	at, known := r.categories[key]
	if !known {
		r.categories[key] = len(r.DiscardSummary)
		r.DiscardSummary = append(r.DiscardSummary, DiscardCategory{
			Reason: reason, Count: 1, ByDesign: discard.ByDesign,
		})
		return
	}
	r.DiscardSummary[at].Count++
}

// ingestOne reads one artefact and writes it, with its state, in one transaction.
//
// State and data commit together on purpose: committing the state apart would let
// a crash between the two leave a fingerprint saying "synced" over data that was
// never written, and that file would then be skipped forever.
func ingestOne(ctx context.Context, db Database, layers layerResolver, opts Options,
	target Target, fingerprint string, result *Result) error {
	records, reason := read(ctx, opts, target, result)
	if reason != "" {
		result.fail(target, reason)
		// The failure is recorded against the path so the next run reads the file
		// again instead of trusting a fingerprint it never earned.
		return db.Write(ctx, func(tx *sql.Tx) error {
			return RecordState(ctx, tx, target, fingerprint, reason, nil)
		})
	}
	kept := records.Sessions[:0]
	for _, session := range records.Sessions {
		if session.ID == "" {
			records.Discards = append(records.Discards, parsers.Discard{
				Reason: "session has no identity",
			})
			continue
		}
		kept = append(kept, session)
	}
	records.Sessions = kept
	result.ExchangesHeld += records.Deferred
	result.discard(target, records.Discards)

	var counts Counts
	err := db.Write(ctx, func(tx *sql.Tx) error {
		written, err := WriteRecords(ctx, tx, layers, records)
		if err != nil {
			return err
		}
		counts = written
		summary := map[string]any{
			"sessions":  written.Sessions,
			"exchanges": written.Exchanges,
			"memories":  written.MemoriesInserted + written.MemoriesUpdated,
		}
		return RecordState(ctx, tx, target, fingerprint, "", summary)
	})
	if err == nil {
		result.recordWritten(target, counts)
	}
	return err
}

// read turns one artefact into records; what the content declares outranks what
// the path encodes.
func read(ctx context.Context, opts Options, target Target, result *Result) (parsers.Records, string) {
	var databaseReader func(context.Context, string) (parsers.Records, []string, error)
	switch target.Kind {
	case parsers.KindOpenCodeDB:
		databaseReader = ReadOpenCode
	case parsers.KindHermesDB:
		databaseReader = ReadHermes
	}
	if databaseReader != nil {
		records, complaints, err := databaseReader(ctx, target.Path)
		if err != nil {
			return parsers.Records{}, err.Error()
		}
		result.Warnings = append(result.Warnings, complaints...)
		for _, complaint := range complaints {
			records.Discards = append(records.Discards, foreignDiscard(complaint))
		}
		return records, ""
	}

	if target.Kind == parsers.KindClaudeWebConversations ||
		target.Kind == parsers.KindClaudeWebMemories ||
		target.Kind == parsers.KindChatGPTWebConversations {
		file, err := os.Open(target.Path)
		if err != nil {
			return parsers.Records{}, err.Error()
		}
		defer file.Close()
		meta := parsers.FileMeta{
			Path: target.Path, FileName: target.FileName, SourceAgent: target.SourceAgent,
		}
		var records parsers.Records
		if target.Kind == parsers.KindClaudeWebConversations {
			records, err = parsers.ParseClaudeWebConversations(file, meta)
		} else if target.Kind == parsers.KindClaudeWebMemories {
			records, err = parsers.ParseClaudeWebMemories(file, meta)
		} else {
			records, err = parsers.ParseChatGPTWebConversations(file, meta)
		}
		if err != nil {
			return parsers.Records{}, err.Error()
		}
		resolveProjects(ctx, opts, target, &records)
		return records, ""
	}

	content, err := os.ReadFile(target.Path)
	if err != nil {
		return parsers.Records{}, err.Error()
	}

	// A subagent transcript is proved to be one before any parser reads it: a
	// foreign transcript under a shared root would otherwise be parsed as a Claude
	// one merely because it also ends in `.jsonl`.
	if target.Kind == parsers.KindSubagent {
		confirmed, known := parsers.LooksLikeSubagent(content)
		if !confirmed && known {
			return parsers.Records{}, "it is not a Claude subagent transcript"
		}
		if !known {
			return parsers.Records{Discards: []parsers.Discard{{
				Reason: "subagent shape could not be identified in the probe window",
			}}}, ""
		}
	}

	meta := parsers.FileMeta{
		Path:        target.Path,
		FileName:    target.FileName,
		SessionID:   target.SessionID,
		Project:     target.Project,
		SourceAgent: target.SourceAgent,
		SourceType:  target.SourceType,
	}
	if target.SidecarPath != "" {
		meta.Sidecar, err = os.ReadFile(target.SidecarPath)
		if err != nil {
			result.fail(Target{Path: target.SidecarPath, Kind: parsers.KindSessionMetadata}, err.Error())
		}
	}

	records, err := parsers.Parse(target.Kind, content, meta)
	if err != nil {
		return parsers.Records{}, err.Error()
	}
	resolveProjects(ctx, opts, target, &records)
	return records, ""
}

// resolveProjects settles each session's project with this precedence:
// the working directory the artefact itself declares, then the identity the scan
// declared, then what the path encodes. The path is the last resort because its
// encoding is lossy.
func resolveProjects(ctx context.Context, opts Options, target Target, records *parsers.Records) {
	for i := range records.Memories {
		memory := &records.Memories[i]
		cwd, _ := memory.Metadata["cwd"].(string)
		if memory.Project == "" && cwd != "" {
			memory.Project = ProjectFromCwd(cwd)
		}
	}
	for i := range records.Sessions {
		session := &records.Sessions[i]
		if target.Kind == parsers.KindClaudeWebConversations ||
			target.Kind == parsers.KindChatGPTWebConversations {
			// An export path says nothing about the conversation's project. It is
			// deliberately not passed through path heuristics in v1.
			continue
		}
		cwd, _ := session.Metadata["cwd"].(string)
		fromContent := ""
		switch target.Kind {
		case parsers.KindCodexSession, parsers.KindPiSession:
			fromContent = ProjectFromCwd(cwd)
		default:
			fromContent = ProjectFromMetadataCwd(cwd)
		}
		if fromContent != "" {
			session.Project = fromContent
		} else if session.Project == "" {
			if fromPath, ok := ProjectFromPath(target.Path, opts.Roots.Workspace); ok {
				session.Project = fromPath
			}
		}
		if target.Kind == parsers.KindCodexSession {
			enrichCodexSession(ctx, opts, target, session)
		}
	}
}

// enrichCodexSession merges what Codex's state database knows. The nickname is
// what turns `codex` into a named agent, and it may upgrade a row this ingest
// itself wrote as generic before the state database existed.
func enrichCodexSession(ctx context.Context, opts Options, target Target, session *parsers.Session) {
	enrichment := enrichCodex(ctx, opts.Roots.CodexStateDB, session.ID, target.Path)
	if len(enrichment.Metadata) == 0 {
		return
	}
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	// The state database is the structured source and it wins over what the
	// rollout's own text said, but a value it does not have never erases one the
	// rollout did.
	maps.Copy(session.Metadata, enrichment.Metadata)
	if enrichment.SourceAgent != "" {
		session.SourceAgent = enrichment.SourceAgent
		session.AgentMayUpgrade = true
	}
}

func tableCounts(ctx context.Context, db *sql.DB) (Tables, error) {
	var counts Tables
	for table, into := range map[string]*int{
		"memories":        &counts.Memories,
		"sessions":        &counts.Sessions,
		"exchanges":       &counts.Exchanges,
		"thinking_blocks": &counts.ThinkingBlocks,
		"tool_uses":       &counts.ToolUses,
	} {
		// The table name is not user input: it is one of these five literals.
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(into); err != nil {
			return counts, fmt.Errorf("count the rows of %s: %w", table, err)
		}
	}
	return counts, nil
}

// declaredRoots is where the run looked. It is reported because "it found nothing"
// and "it looked somewhere else" are the two explanations of an empty ingest, and
// an operator cannot tell them apart without this.
func declaredRoots(roots Roots) map[string]string {
	declared := map[string]string{
		"claude_projects":         roots.ClaudeProjects,
		"claude_desktop_sessions": roots.ClaudeDesktopSessions,
		"cowork_sessions":         roots.CoworkSessions,
		"codex_root":              roots.CodexRoot,
		"codex_sessions":          roots.CodexSessions,
		"opencode_db":             roots.OpenCodeDB,
		"pi_sessions":             roots.PiSessions,
		"hermes_db":               roots.HermesDB,
		"anthropic_export_paths":  strings.Join(roots.ClaudeWebExports, string(os.PathListSeparator)),
		"openai_export_paths":     strings.Join(roots.ChatGPTWebExports, string(os.PathListSeparator)),
	}
	maps.DeleteFunc(declared, func(_, value string) bool { return value == "" })
	return declared
}

// SortedSources is the report's source names in a stable order, for the readable
// output.
func SortedSources(sources map[string]*Counts) []string {
	return slices.Sorted(maps.Keys(sources))
}
