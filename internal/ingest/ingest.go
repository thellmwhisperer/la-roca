package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"os"
	"slices"
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
}

// Failure is one artefact that could not be read, named so the operator knows
// which one and why. One bad file is isolated and reported; it never costs the
// run.
type Failure struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Reason string `json:"error"`
}

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
	// Scanned is what the roots hold, per source, always with every source
	// present: a source missing from the report reads as one nobody looked at.
	Scanned map[string]int `json:"scanned"`
	// Sources is what each source wrote. It is what F02-09 reads as "a count for
	// every seeded source".
	Sources map[string]*Counts `json:"sources"`
	// Files says how the run divided its work: what it read, what it skipped by
	// fingerprint, and what a source is still writing.
	FilesRead     int `json:"files_read"`
	FilesSkipped  int `json:"files_skipped"`
	ExchangesHeld int `json:"exchanges_deferred"`

	Before Tables `json:"counts_before"`
	After  Tables `json:"counts_after"`
	Delta  Tables `json:"delta"`

	WorkspaceRoots Workspace         `json:"workspace_roots"`
	DetectedAgents []string          `json:"detected_agents"`
	Roots          map[string]string `json:"roots"`
	Warnings       []string          `json:"warnings,omitempty"`
	ElapsedMS      int64             `json:"elapsed_ms"`
}

// Run reads every source in the matrix once and writes what changed.
//
// The order of the sources is the laboratory's: memories, then the files agents
// keep as configuration, then the transcripts. It matters for one thing only, and
// it is the session titles: the transcript writes the session and the metadata
// file names it, so the first non-blank name wins deterministically.
func Run(ctx context.Context, db Database, layers layerResolver, opts Options) (Result, error) {
	start := time.Now()
	plan := Scan(opts.Roots)

	result := Result{
		DryRun:  opts.DryRun,
		Scanned: plan.Scanned,
		Sources: map[string]*Counts{},
		WorkspaceRoots: Workspace{
			Selected: orEmpty(plan.WorkspaceRoots),
		},
		DetectedAgents: orEmpty(plan.DetectedAgents),
		Roots:          declaredRoots(opts.Roots),
		Warnings:       plan.Warnings,
	}
	announced := map[string]bool{}
	for _, target := range plan.Targets {
		result.source(target.SourceAgent)
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
	}
	result.Before = before
	result.After = before

	for _, target := range plan.Targets {
		fingerprint, err := Fingerprint(target.Path)
		if err != nil {
			// The file was there when the scan ran and is not there now. That is a
			// live disk, not an error worth a red run.
			result.FilesSkipped++
			continue
		}
		if Unchanged(state, target.Path, fingerprint) {
			result.FilesSkipped++
			continue
		}
		if opts.DryRun {
			result.FilesRead++
			continue
		}
		if opts.Progress != nil && !announced[target.SourceAgent] {
			opts.Progress("ingest: reading " + target.SourceAgent)
			announced[target.SourceAgent] = true
		}
		result.FilesRead++
		if err := ingestOne(ctx, db, layers, opts, target, fingerprint, &result); err != nil {
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
	result.ElapsedMS = time.Since(start).Milliseconds()
	return result, nil
}

// source makes sure a source that was scanned appears in the report even when it
// wrote nothing.
func (r *Result) source(agent string) *Counts {
	if agent == "" {
		agent = "unknown"
	}
	if counts, ok := r.Sources[agent]; ok {
		return counts
	}
	counts := &Counts{}
	r.Sources[agent] = counts
	return counts
}

func (r *Result) fail(target Target, reason string) {
	r.Errors++
	r.ErrorDetails = append(r.ErrorDetails, Failure{
		Path: target.Path, Kind: string(target.Kind), Reason: reason,
	})
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
	result.ExchangesHeld += records.Deferred

	return db.Write(ctx, func(tx *sql.Tx) error {
		counts, err := WriteRecords(ctx, tx, layers, records)
		if err != nil {
			return err
		}
		result.source(target.SourceAgent).add(counts)
		summary := map[string]any{
			"sessions":  counts.Sessions,
			"exchanges": counts.Exchanges,
			"memories":  counts.MemoriesInserted + counts.MemoriesUpdated,
		}
		return RecordState(ctx, tx, target, fingerprint, "", summary)
	})
}

// read turns one artefact into records, resolving the project the way the
// laboratory does: what the content declares outranks what the path encodes.
func read(ctx context.Context, opts Options, target Target, result *Result) (parsers.Records, string) {
	switch target.Kind {
	case parsers.KindOpenCodeDB:
		records, complaints, err := ReadOpenCode(ctx, target.Path)
		if err != nil {
			return parsers.Records{}, err.Error()
		}
		result.Warnings = append(result.Warnings, complaints...)
		return records, ""
	case parsers.KindHermesDB:
		records, complaints, err := ReadHermes(ctx, target.Path)
		if err != nil {
			return parsers.Records{}, err.Error()
		}
		result.Warnings = append(result.Warnings, complaints...)
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
			return parsers.Records{}, ""
		}
	}

	meta := parsers.FileMeta{
		Path:        target.Path,
		FileName:    target.FileName,
		SessionID:   target.SessionID,
		Project:     target.Project,
		SourceAgent: target.SourceAgent,
		SourceType:  target.SourceType,
		SkillName:   target.SkillName,
	}
	if target.SidecarPath != "" {
		meta.Sidecar, _ = os.ReadFile(target.SidecarPath)
	}

	records, err := parsers.Parse(target.Kind, content, meta)
	if err != nil {
		return parsers.Records{}, err.Error()
	}
	resolveProjects(ctx, opts, target, &records)
	return records, ""
}

// resolveProjects settles each session's project with the laboratory's precedence:
// the working directory the artefact itself declares, then the identity the scan
// declared, then what the path encodes. The path is the last resort because its
// encoding is lossy.
func resolveProjects(ctx context.Context, opts Options, target Target, records *parsers.Records) {
	for i := range records.Sessions {
		session := &records.Sessions[i]
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
	}
	maps.DeleteFunc(declared, func(_, value string) bool { return value == "" })
	return declared
}

// SortedSources is the report's source names in a stable order, for the readable
// output.
func SortedSources(sources map[string]*Counts) []string {
	return slices.Sorted(maps.Keys(sources))
}
