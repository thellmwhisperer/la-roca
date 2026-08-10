package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestTheWholeMatrixIsIngested(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()

	result, err := Run(ctx, db, registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Errors != 0 {
		t.Fatalf("errors = %d: %+v", result.Errors, result.ErrorDetails)
	}

	// Every source of the matrix is scanned, and every family
	// wrote something. A family missing from here is a family that has been lost.
	for key, want := range map[string]int{
		"claude_memory_files":  1, // one project file; not MEMORY.md and not the global CLAUDE.md
		"codex_files":          3, // one memory, one rule, one skill; not default.rules
		"session_files":        1,
		"codex_session_files":  1,
		"claude_desktop_files": 1,
		"cowork_files":         2, // the metadata and the audit transcript it pairs with
		"subagent_files":       1,
		"pi_session_files":     1,
		"opencode_databases":   1,
		"hermes_databases":     1,
	} {
		if got := result.Scanned[key]; got != want {
			t.Errorf("scanned[%s] = %d, want %d", key, got, want)
		}
	}

	// The families, by the source agent each of them writes under.
	for _, agent := range []string{
		"claude", "claude-desktop", "cowork", "codex", "pi", "opencode", "hermes",
	} {
		counts, ok := result.Sources[agent]
		if !ok {
			t.Errorf("the source %q is not in the report: %v", agent, SortedSources(result.Sources))
			continue
		}
		if counts.Sessions+counts.SessionsUpdated+counts.MemoriesInserted == 0 {
			t.Errorf("the source %q wrote nothing: %+v", agent, counts)
		}
	}

	// Six sessions: the Claude transcript, its subagent, the Codex rollout, the
	// Cowork one, Pi's and OpenCode's. The desktop metadata names the Claude
	// transcript's own session instead of opening another one, and the Hermes one
	// is the eighth. The count is asserted whole so a source that stops writing is
	// visible here and not three waves later.
	if result.Delta.Sessions != 7 {
		t.Errorf("sessions = %d, want 7", result.Delta.Sessions)
	}
	// Four memories: the per-project Claude memory file plus the three Codex
	// memories/rules/skills. The global CLAUDE.md and the repository instructions
	// are configuration, not content.
	if result.Delta.Memories != 4 {
		t.Errorf("memories = %d, want 4", result.Delta.Memories)
	}
	if got := countRows(t, db.SQL(), "memories WHERE source_agent = 'config'"); got != 0 {
		t.Errorf("config memories = %d, want none", got)
	}
	// The global CLAUDE.md is configuration: its text never reaches the corpus.
	if got := countRows(t, db.SQL(), `memories WHERE content LIKE '%Always TDD%'`); got != 0 {
		t.Errorf("the global CLAUDE.md was ingested as a memory: %d row(s)", got)
	}
	if result.Delta.Exchanges == 0 || result.Delta.ThinkingBlocks == 0 || result.Delta.ToolUses == 0 {
		t.Errorf("delta = %+v: something stopped writing its children", result.Delta)
	}

	// The project decodes against the declared workspace root, and no session is
	// left carrying an encoded absolute path as if it were a project name.
	projects := queryColumn(t, db.SQL(),
		`SELECT DISTINCT COALESCE(project, '') FROM sessions ORDER BY 1`)
	for _, project := range projects {
		if strings.HasPrefix(project, "-") || strings.Contains(project, "/") {
			t.Errorf("a session is filed under %q, which is a path and not a project", project)
		}
	}
	if !containsString(projects, "demo") {
		t.Errorf("projects = %v, want demo among them", projects)
	}

	// The tool verdict travels: the Claude call succeeded and is stored as such.
	if got := queryColumn(t, db.SQL(), `SELECT tool_name FROM tool_uses ORDER BY tool_name`); len(got) == 0 {
		t.Error("no tool use was written")
	}

	// The Hermes session Hermes has not closed is not read.
	if containsString(queryColumn(t, db.SQL(), `SELECT session_id FROM sessions`), "h2") {
		t.Error("a live Hermes session was ingested before it had an ending")
	}
}

func TestUnreadableCoworkSidecarIsCountedPerFile(t *testing.T) {
	world := newWorld(t)
	path := filepath.Join(world.roots().CoworkSessions, "cw.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	result, err := Run(context.Background(), rocaDatabase(t), registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, failure := range result.ErrorDetails {
		if failure.Path == path && failure.Parser == string(parsers.KindSessionMetadata) && failure.Reason != "" {
			found = true
		}
	}
	if !found || result.Errors == 0 {
		t.Fatalf("sidecar read failure was not counted: errors=%d details=%+v", result.Errors, result.ErrorDetails)
	}
}

// The contract of requirement M2, and it is a test and not an aspiration: running
// the ingest twice over the same disk produces exactly the same state.
func TestASecondPassOverTheSameDiskChangesNothing(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()
	options := Options{Roots: world.roots()}

	first, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.FilesRead == 0 {
		t.Fatal("the first run read nothing")
	}
	before := contentFingerprint(t, db.SQL())

	second, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	// Zero duplicates: the delta is zero in every category.
	if second.Delta != (Tables{}) {
		t.Errorf("delta = %+v, want zero in every category", second.Delta)
	}
	// Zero rewrites: the data is byte for byte the same, which is stronger than
	// the counts matching. Two rows swapped for two others would keep the count.
	if after := contentFingerprint(t, db.SQL()); after != before {
		t.Error("the second pass rewrote data that had not changed")
	}
	// And fast: not one file was opened, because the fingerprint answered first.
	if second.FilesRead != 0 {
		t.Errorf("files read = %d, want none: the fingerprint has to answer without opening them",
			second.FilesRead)
	}
	if second.FilesSkipped != first.FilesRead {
		t.Errorf("files skipped = %d, want the %d the first pass read",
			second.FilesSkipped, first.FilesRead)
	}
	if second.Errors != 0 {
		t.Errorf("errors = %d: %+v", second.Errors, second.ErrorDetails)
	}
}

// Every path of every source belongs in the state table, regardless of the route
// that discovered it.
func TestEveryPathOfEverySourceIsFingerprinted(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()

	result, err := Run(ctx, db, registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var recorded int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM ingest_file_state WHERE fingerprint IS NOT NULL AND fingerprint <> ''`).
		Scan(&recorded); err != nil {
		t.Fatalf("count the state: %v", err)
	}
	if recorded != result.FilesRead {
		t.Errorf("state rows = %d, want the %d files the run read", recorded, result.FilesRead)
	}

	kinds := queryColumn(t, db.SQL(), `SELECT DISTINCT source_kind FROM ingest_file_state ORDER BY 1`)
	for _, kind := range []string{
		"claude_memory", "claude_session", "codex_file", "codex_session",
		"cowork_audit", "hermes_database", "opencode_database", "pi_session",
		"session_metadata", "subagent",
	} {
		if !containsString(kinds, kind) {
			t.Errorf("the source kind %q has no state row: %v", kind, kinds)
		}
	}
}

// A file that grew is read again, and only its new exchanges land.
func TestAGrownTranscriptOnlyAddsWhatIsNew(t *testing.T) {
	world, db, ctx, options := seededWorld(t)
	before := countRows(t, db.SQL(), "exchanges")

	transcript := filepath.Join(world.roots().ClaudeProjects, world.projectDir(),
		fixtureSessionID+".jsonl")
	appendTo(t, transcript, `
{"type":"user","timestamp":"2026-08-01T10:05:00Z","message":{"content":[{"type":"text","text":"and the verification"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:05:01Z","message":{"content":[{"type":"text","text":"against a synthetic tree"}]}}
`)

	second, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Delta.Exchanges != 1 {
		t.Errorf("exchanges = %d, want exactly the new one", second.Delta.Exchanges)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != before+1 {
		t.Errorf("exchanges = %d, want %d", got, before+1)
	}
	// And nothing else was re-read: one file changed, one file was read.
	if second.FilesRead != 1 {
		t.Errorf("files read = %d, want 1", second.FilesRead)
	}
}

// The duplication shield covers three routes: the same session reached by a re-run, by a
// re-scan of rewritten content, and by a copy under a second path, leaves not one
// duplicate row and not one rewrite.
func TestTheSameSessionReachedAgainLeavesNoDuplicateRows(t *testing.T) {
	world, db, ctx, options := seededWorld(t)

	transcript := filepath.Join(world.roots().ClaudeProjects, world.projectDir(),
		fixtureSessionID+".jsonl")
	original, err := os.ReadFile(transcript)
	if err != nil {
		t.Fatalf("read the transcript: %v", err)
	}
	baseline := contentFingerprint(t, db.SQL())
	baselineDupes := duplicateExchanges(t, db.SQL())

	// --- 1. a re-run touches nothing, because the fingerprint answers first ---
	again, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if again.FilesRead != 0 {
		t.Errorf("re-run read %d files, want none: the fingerprint answers first", again.FilesRead)
	}
	assertNoNewDuplicates(t, db.SQL(), baselineDupes, baseline, "a re-run")

	// --- 2. the same content re-scanned, with the fingerprint defeated ---
	// A file rewritten with identical bytes still carries a new mtime, so the
	// file-level skip does not apply and the record-level shield must answer.
	if err := os.WriteFile(transcript, original, 0o600); err != nil {
		t.Fatalf("rewrite the transcript: %v", err)
	}
	touchFuture(t, transcript)
	rescan, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("re-scan: %v", err)
	}
	if rescan.FilesRead == 0 {
		t.Fatal("the re-scan did not re-read the rewritten file, so it proves nothing")
	}
	assertNoNewDuplicates(t, db.SQL(), baselineDupes, baseline, "a re-scan of the same content")

	// --- 3. a copy of the same session under a second path ---
	// Same content, same session id, a path the state table has never seen: only
	// the record-level shield can keep it from landing a second time.
	secondDir := filepath.Join(world.roots().ClaudeProjects, encodeRoot(filepath.Join(world.workspace, "second")))
	world.write(t, filepath.Join(secondDir, fixtureSessionID+".jsonl"), string(original))
	recopy, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("copy under a second path: %v", err)
	}
	if recopy.FilesRead == 0 {
		t.Fatal("the copy under a second path was not read, so it proves nothing")
	}
	assertNoNewDuplicates(t, db.SQL(), baselineDupes, baseline, "a copy under a second path")
}

// A dry run never writes and still reports what it would do under JSON output.
func TestTheDryRunWritesNothingAndAnswersHonestly(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()
	options := Options{Roots: world.roots(), DryRun: true}

	before := contentFingerprint(t, db.SQL())
	result, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !result.DryRun {
		t.Error("the result does not declare itself a dry run")
	}
	if result.Delta != (Tables{}) {
		t.Errorf("delta = %+v, want zero: a dry run writes nothing", result.Delta)
	}
	if after := contentFingerprint(t, db.SQL()); after != before {
		t.Error("the dry run touched the database")
	}
	if countRows(t, db.SQL(), "ingest_file_state") != 0 {
		t.Error("the dry run wrote ingest state")
	}
	// It still counts, which is the whole point of asking.
	if result.FilesRead == 0 {
		t.Error("the dry run reports nothing to read")
	}
	if result.Scanned["claude_memory_files"] == 0 {
		t.Errorf("scanned = %+v", result.Scanned)
	}

	// After a real run, the dry run says there is nothing left to do.
	if _, err := Run(ctx, db, registry(t), Options{Roots: world.roots()}); err != nil {
		t.Fatalf("real run: %v", err)
	}
	again, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("second dry run: %v", err)
	}
	if again.FilesRead != 0 {
		t.Errorf("files read = %d, want none left", again.FilesRead)
	}
}

// A dry run over a database with no schema in it still answers. It is the mode an
// operator reaches for when something is wrong, so it cannot be the mode that
// needs everything to be right.
func TestTheDryRunAnswersOverADatabaseItCannotRead(t *testing.T) {
	world := newWorld(t)
	empty := emptyDatabase(t)
	result, err := Run(context.Background(), empty, registry(t),
		Options{Roots: world.roots(), DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.FilesRead == 0 {
		t.Error("it reported nothing to read")
	}
	if len(result.Warnings) == 0 {
		t.Error("it did not say that it could not read the state")
	}
}

// One unreadable artefact is isolated and named. It does not take the run down and
// it does not earn a fingerprint either, so the next run looks again.
func TestOneBadFileIsIsolatedAndNamed(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()

	// A foreign transcript under the subagents root: same extension, another shape.
	world.write(t, filepath.Join(world.roots().ClaudeProjects, world.projectDir(),
		fixtureSessionID, "subagents", "foreign.jsonl"),
		`{"event":"turn","payload":{"role":"user","text":"something else"}}`+"\n")

	result, err := Run(ctx, db, registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Errors != 1 {
		t.Fatalf("errors = %d, want 1: %+v", result.Errors, result.ErrorDetails)
	}
	if !strings.Contains(result.ErrorDetails[0].Path, "foreign.jsonl") {
		t.Errorf("the error does not name the file: %+v", result.ErrorDetails[0])
	}
	// The rest of the run landed.
	if result.Delta.Sessions == 0 {
		t.Error("one bad file took the batch down")
	}
	// And the failure is recorded, so the next run does not trust a fingerprint it
	// never earned.
	var failure string
	if err := db.SQL().QueryRow(
		`SELECT COALESCE(last_error,'') FROM ingest_file_state WHERE path LIKE '%foreign.jsonl'`).
		Scan(&failure); err != nil {
		t.Fatalf("read the state: %v", err)
	}
	if failure == "" {
		t.Error("the failure was not recorded against the path")
	}

	second, err := Run(ctx, db, registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.FilesRead != 1 {
		t.Errorf("files read = %d, want the failed one read again", second.FilesRead)
	}
}

// A project directory no declared root explains produces a diagnosis with a
// remedy, and the session is stored with no project rather than with a path.
func TestAnAmbiguousProjectDirectoryIsDiagnosedAndNotInvented(t *testing.T) {
	world := newWorld(t)
	db := rocaDatabase(t)
	orphan := "-somewhere-nobody-declared"
	world.write(t, filepath.Join(world.roots().ClaudeProjects, orphan,
		"99999999-8888-7777-6666-555555555555.jsonl"), `
{"type":"user","timestamp":"2026-08-01T10:00:00Z","message":{"content":"hello"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"text","text":"bye"}]}}
`)

	result, err := Run(context.Background(), db, registry(t), Options{Roots: world.roots()})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !warnsAbout(result.Warnings, "workspace_roots") {
		t.Errorf("no diagnosis names the remedy: %v", result.Warnings)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, orphan) {
			t.Errorf("the diagnosis carries the encoded path: %q", warning)
		}
	}
	var project sql.NullString
	if err := db.SQL().QueryRow(
		`SELECT project FROM sessions WHERE session_id = '99999999-8888-7777-6666-555555555555'`).
		Scan(&project); err != nil {
		t.Fatalf("read the session: %v", err)
	}
	if project.Valid && project.String != "" {
		t.Errorf("project = %q, want none: an encoded path is not a project name", project.String)
	}
}

// --- helpers ---

// contentFingerprint hashes every row of every table the ingest writes. It is what
// makes "it rewrote nothing" measurable instead of assumed.
func contentFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	hash := sha256.New()
	for _, statement := range []string{
		`SELECT session_id, COALESCE(source_agent,''), COALESCE(project,''),
		        COALESCE(started_at,''), COALESCE(ended_at,''), COALESCE(duration_minutes,-1),
		        COALESCE(title,''), COALESCE(metadata,'') FROM sessions ORDER BY session_id`,
		`SELECT session_id, COALESCE(exchange_number,-1), COALESCE(human_text,''),
		        COALESCE(agent_text,''), COALESCE(human_timestamp,''), COALESCE(agent_timestamp,''),
		        COALESCE(response_latency_ms,-1), is_after_compaction FROM exchanges
		 ORDER BY session_id, exchange_number`,
		`SELECT session_id, COALESCE(exchange_number,-1), full_text, word_count
		 FROM thinking_blocks ORDER BY session_id, exchange_number, full_text`,
		`SELECT session_id, COALESCE(exchange_number,-1), tool_name,
		        COALESCE(tool_params_summary,''), had_error FROM tool_uses
		 ORDER BY session_id, exchange_number, tool_name`,
		`SELECT layer, content, COALESCE(metadata,''), origin, COALESCE(source_agent,''),
		        COALESCE(project,''), status FROM memories ORDER BY content`,
	} {
		rows, err := db.Query(statement)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(columns))
			for i := range cells {
				cells[i] = new(any)
			}
			if err := rows.Scan(cells...); err != nil {
				rows.Close()
				t.Fatalf("fingerprint: %v", err)
			}
			for _, cell := range cells {
				fmt.Fprintf(hash, "%v\x00", *(cell.(*any)))
			}
			fmt.Fprint(hash, "\x01")
		}
		rows.Close()
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func queryColumn(t *testing.T, db *sql.DB, statement string) []string {
	t.Helper()
	rows, err := db.Query(statement)
	if err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func appendTo(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatalf("append to %s: %v", path, err)
	}
}

// touchFuture moves a file's mtime clearly forward so its fingerprint (size:mtime)
// changes even when the bytes are identical, defeating the file-level skip.
func touchFuture(t *testing.T, path string) {
	t.Helper()
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("set the mtime of %s: %v", path, err)
	}
}

// duplicateExchanges counts repeated text groups within the same session.
func duplicateExchanges(t *testing.T, db *sql.DB) int {
	t.Helper()
	var dupes int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM (
		  SELECT session_id, human_text, agent_text FROM exchanges
		  WHERE COALESCE(human_text,'') <> '' OR COALESCE(agent_text,'') <> ''
		  GROUP BY session_id, human_text, agent_text HAVING COUNT(*) > 1)`).
		Scan(&dupes); err != nil {
		t.Fatalf("count duplicate exchanges: %v", err)
	}
	return dupes
}

// assertNoNewDuplicates holds both halves of the shield: no exchange text is
// stored more than once in the same session (the data the operator asked for), and
// the whole corpus is byte for byte what it was (no duplicate landed and nothing
// already there was rewritten).
func assertNoNewDuplicates(t *testing.T, db *sql.DB, baselineDupes int, baselineFingerprint, phase string) {
	t.Helper()
	if got := duplicateExchanges(t, db); got != baselineDupes {
		t.Errorf("after %s there are %d duplicate exchange groups, want the %d the baseline had",
			phase, got, baselineDupes)
	}
	if got := contentFingerprint(t, db); got != baselineFingerprint {
		t.Errorf("after %s the corpus changed: a re-reach rewrote or duplicated data", phase)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func warnsAbout(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
}

// emptyDatabase is a database with no schema in it at all.
func emptyDatabase(t *testing.T) Database {
	t.Helper()
	path := filepath.Join(t.TempDir(), "no-schema.db")
	handle, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { handle.Close() })
	if err := handle.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return bareDatabase{handle}
}

type bareDatabase struct{ handle *sql.DB }

func (b bareDatabase) SQL() *sql.DB { return b.handle }

func (b bareDatabase) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := b.handle.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// A source that is a live database, not a file, is refused whole when its shape
// is not the one this build reads. Half a foreign table produces rows nobody can
// trust, and the refusal has to name the agent so the operator knows which one
// migrated its schema under us.
func TestAForeignDatabaseWhoseShapeChangedIsRefusedByName(t *testing.T) {
	for _, one := range []struct {
		source string
		schema []string
		read   func(context.Context, string) (parsers.Records, []string, error)
		absent string
	}{
		{
			source: "OpenCode",
			schema: []string{
				`CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT)`,
				`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
				  directory TEXT, version TEXT, time_created INTEGER, time_updated INTEGER, agent TEXT)`,
				`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT,
				  time_created INTEGER, time_updated INTEGER, data TEXT)`,
				// `part` lost the column that carries the content.
				`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
				  time_created INTEGER, time_updated INTEGER)`,
			},
			read:   ReadOpenCode,
			absent: "data",
		},
		{
			source: "Hermes",
			schema: []string{
				`CREATE TABLE sessions (id TEXT PRIMARY KEY, started_at REAL, ended_at REAL)`,
				// `messages` lost the column that orders a session.
				`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT)`,
			},
			read:   ReadHermes,
			absent: "timestamp",
		},
	} {
		t.Run(one.source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "foreign.db")
			db := openSynthetic(t, path)
			for _, statement := range one.schema {
				exec(t, db, statement)
			}
			db.Close()

			_, _, err := one.read(context.Background(), path)
			if err == nil {
				t.Fatal("a database this build cannot read was accepted")
			}
			if !strings.Contains(err.Error(), one.source) {
				t.Errorf("the refusal does not name the source: %v", err)
			}
			if !strings.Contains(err.Error(), one.absent) {
				t.Errorf("the refusal does not name the missing column %q: %v", one.absent, err)
			}
		})
	}
}

// DiscardDetail.Record is documented as the record position, and an operator
// reads it that way: "record 2" sends them to the second record of the source.
// A foreign database has no line numbers, and the complaint's own index in the
// complaint list was being handed over as if it were one, so the first two
// skipped sessions of any OpenCode database were reported as records 1 and 2 of
// a file that has neither.
func TestAForeignDatabaseComplaintDoesNotInventARecordPosition(t *testing.T) {
	records := parsers.Records{}
	complaints := []string{
		"OpenCode session ses_a: data is not an object",
		"OpenCode session ses_b: data is not an object",
	}
	for _, complaint := range complaints {
		records.Discards = append(records.Discards, foreignDiscard(complaint))
	}
	for _, discard := range records.Discards {
		if discard.Record != 0 {
			t.Errorf("a database complaint claims record %d: %q",
				discard.Record, discard.Reason)
		}
	}
}

func TestHermesMissingStartDoesNotBecomeTheUnixEpoch(t *testing.T) {
	session := hermesSession(row{"id": "h-missing", "started_at": nil, "ended_at": float64(10)}, nil)
	if session.StartedAt != "" {
		t.Fatalf("missing started_at became %q", session.StartedAt)
	}
	if session.DurationMinutes != nil {
		t.Fatalf("duration from a missing start = %v", session.DurationMinutes)
	}
}

func TestHermesKeepsActiveFilterWhenMessagesHaveNoID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes.db")
	db := openSynthetic(t, path)
	exec(t, db, `CREATE TABLE sessions (id TEXT, started_at REAL, ended_at REAL)`)
	exec(t, db, `CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT,
		timestamp REAL, active INTEGER)`)
	exec(t, db, `INSERT INTO sessions VALUES ('h1', 10, 20)`)
	exec(t, db, `INSERT INTO messages VALUES ('h1', 'user', 'question', 10, 1)`)
	exec(t, db, `INSERT INTO messages VALUES ('h1', 'assistant', 'active answer', 11, 1)`)
	exec(t, db, `INSERT INTO messages VALUES ('h1', 'assistant', 'rewound answer', 12, 0)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	records, complaints, err := ReadHermes(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 0 {
		t.Fatalf("complaints = %v", complaints)
	}
	got := records.Sessions[0].Exchanges[0].AgentText
	if got != "active answer" {
		t.Fatalf("answer = %q, want active answer", got)
	}
}

func TestDatabaseReadersCountLiveTurnsAsDeferred(t *testing.T) {
	user := openCodeRow{id: "user-1"}
	user.message.Role = "user"
	exchanges, deferred := openCodeExchanges([]openCodeRow{user}, nil)
	if deferred != 1 || len(exchanges) != 0 {
		t.Fatalf("OpenCode deferred = %d, exchanges = %d", deferred, len(exchanges))
	}

	path := filepath.Join(t.TempDir(), "hermes.db")
	db := openSynthetic(t, path)
	exec(t, db, `CREATE TABLE sessions (id TEXT, started_at REAL, ended_at REAL)`)
	exec(t, db, `CREATE TABLE messages (session_id TEXT, role TEXT, content TEXT, timestamp REAL)`)
	exec(t, db, `INSERT INTO sessions VALUES ('live', 10, NULL)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	records, _, err := ReadHermes(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if records.Deferred != 1 || len(records.Sessions) != 0 {
		t.Fatalf("Hermes deferred = %d, sessions = %d", records.Deferred, len(records.Sessions))
	}
}

// A dry run over a database it cannot read answers anyway, and it says which of
// the two reads failed. The state failure earns a warning; the row counts failed
// in silence, so the report handed over `counts_before` as five zeros with
// nothing to say they are not the truth.
func TestTheDryRunSaysWhenItCouldNotCountTheRowsEither(t *testing.T) {
	world := newWorld(t)
	result, err := Run(context.Background(), emptyDatabase(t), registry(t),
		Options{Roots: world.roots(), DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if result.Before != (Tables{}) {
		t.Fatalf("counts_before = %+v: this database has no tables to count", result.Before)
	}
	var said bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "row counts") {
			said = true
		}
	}
	if !said {
		t.Errorf("zeroed counts are reported as if they were counted: %v", result.Warnings)
	}
}
