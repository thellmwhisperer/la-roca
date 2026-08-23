package ingest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const (
	madreFixtureSession  = "madre-fixture-session"
	madreOverlapSession  = "madre-overlap-session"
	madreHandoffContent  = "synthetic madre handoff for the next agent"
	madreFeedbackContent = "synthetic madre feedback about the ingest route"
	madreCreatedAt       = "2026-08-01 12:00:00"
)

func TestLegacyStoreIngest(t *testing.T) {
	t.Parallel()
	path := seedMadreFixture(t)

	records, discards, err := ReadMadre(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadMadre: %v", err)
	}
	if records.Seen.Sessions != 2 {
		t.Fatalf("seen sessions = %d, want 2", records.Seen.Sessions)
	}
	if len(records.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(records.Sessions))
	}
	session := madreSessionByID(t, records, madreFixtureSession)
	if session.SourceAgent != "claude-code" {
		t.Errorf("session source_agent = %q, want claude-code", session.SourceAgent)
	}
	if session.SourceSurface != ingestprovenance.LegacyStore {
		t.Errorf("session source_surface = %q, want %q", session.SourceSurface, ingestprovenance.LegacyStore)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(session.Exchanges))
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "count the madre rows" || exchange.AgentText != "two sessions" {
		t.Errorf("exchange text = %q / %q", exchange.HumanText, exchange.AgentText)
	}
	if len(exchange.Thinking) != 1 || exchange.Thinking[0].Text != "measure first" {
		t.Errorf("thinking = %+v", exchange.Thinking)
	}
	if len(exchange.Tools) != 1 || exchange.Tools[0].Name != "exec" {
		t.Errorf("tools = %+v", exchange.Tools)
	}

	if len(records.Memories) != 5 {
		t.Fatalf("memories = %d, want 5", len(records.Memories))
	}
	layers := map[string]parsers.Memory{}
	for _, memory := range records.Memories {
		layers[memory.Layer] = memory
		if memory.SourceSurface != ingestprovenance.LegacyStore {
			t.Errorf("memory %q source_surface = %q", memory.Layer, memory.SourceSurface)
		}
		if memory.Source != madreSource || memory.FilePath == "" {
			t.Errorf("memory %q identity = %q %q", memory.Layer, memory.Source, memory.FilePath)
		}
		if memory.Layer != "handover" && memory.Layer != "protocol" &&
			memory.CreatedAt != madreCreatedAt {
			t.Errorf("memory %q created_at = %q", memory.Layer, memory.CreatedAt)
		}
	}
	handoff, ok := layers["handoff"]
	if !ok {
		t.Fatal("handoff memory missing")
	}
	if handoff.Content != madreHandoffContent || handoff.Status != "pending" {
		t.Errorf("handoff = %+v", handoff)
	}
	if _, ok := layers["feedback"]; !ok {
		t.Error("feedback memory missing")
	}
	if _, ok := layers["discovery"]; !ok {
		t.Error("discovery memory missing")
	}
	if _, ok := layers["handover"]; !ok {
		t.Error("handover memory was reclassified")
	}
	if _, ok := layers["protocol"]; !ok {
		t.Error("protocol memory was reclassified")
	}

	if len(discards) != 0 {
		t.Errorf("complaints = %v", discards)
	}
	excluded, unmatchedTools := 0, 0
	for _, discard := range records.Discards {
		if discard.Reason == madreMissingToolExchangeReason {
			if discard.ByDesign {
				t.Errorf("unmatched tool was excluded by design: %+v", discard)
			}
			unmatchedTools++
			continue
		}
		if !discard.ByDesign {
			t.Errorf("unexpected discard: %+v", discard)
		}
		excluded += 1
	}
	if unmatchedTools != 1 {
		t.Errorf("unmatched tool discards = %d, want 1", unmatchedTools)
	}
	if excluded == 0 {
		t.Error("garden and proposal rows were not reported as exclusions by design")
	}

	corpus, ops := madreStores(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: path})
	options := Options{Roots: roots, Ops: ops}
	first, err := Run(context.Background(), corpus, registry(t), options)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if first.Errors != 0 {
		t.Fatalf("first errors = %+v", first.ErrorDetails)
	}
	counts, ok := first.Sources[madreSource]
	if !ok {
		t.Fatalf("source %q missing: %v", madreSource, SortedSources(first.Sources))
	}
	if counts.Sessions != 2 || counts.Exchanges != 2 || counts.ThinkingBlocks != 2 ||
		counts.ToolUses != 2 || counts.MemoriesInserted != 5 {
		t.Fatalf("first source counts = %+v", counts)
	}
	if first.Delta.Sessions != 2 || first.Delta.Exchanges != 2 || first.Delta.Memories != 5 {
		t.Fatalf("first aggregate delta = %+v", first.Delta)
	}
	if first.RecordsDiscarded != 1 {
		t.Errorf("first discarded records = %d, want 1 unmatched tool", first.RecordsDiscarded)
	}

	var surface, agent string
	if err := corpus.SQL().QueryRow(`SELECT source_surface, source_agent FROM sessions WHERE session_id = ?`,
		madreFixtureSession).Scan(&surface, &agent); err != nil {
		t.Fatal(err)
	}
	if surface != ingestprovenance.LegacyStore || agent != "claude-code" {
		t.Errorf("landed session provenance = %q / %q", surface, agent)
	}

	var layer, status, created string
	var expires sql.NullString
	if err := ops.SQL().QueryRow(`
		SELECT layer, status, created_at, expires_at FROM memories WHERE content = ?`,
		madreHandoffContent).Scan(&layer, &status, &created, &expires); err != nil {
		t.Fatal(err)
	}
	if layer != "handoff" {
		t.Errorf("handoff landed in layer %q", layer)
	}
	if status != "pending" || created != madreCreatedAt {
		t.Errorf("handoff status/created_at = %q / %q", status, created)
	}
	if expires.Valid {
		t.Errorf("handoff expires_at = %q, want NULL", expires.String)
	}
	for content, wantLayer := range map[string]string{
		"synthetic legacy handover": "handover",
		"synthetic legacy protocol": "protocol",
	} {
		var landedLayer string
		var landedStatus, landedCreated sql.NullString
		if err := ops.SQL().QueryRow(`SELECT layer, status, created_at FROM memories WHERE content = ?`,
			content).Scan(&landedLayer, &landedStatus, &landedCreated); err != nil {
			t.Fatal(err)
		}
		if landedLayer != wantLayer {
			t.Errorf("%s landed in layer %q", wantLayer, landedLayer)
		}
		if landedStatus.Valid || landedCreated.Valid {
			t.Errorf("%s state = status %q created_at %q, want NULLs",
				wantLayer, landedStatus.String, landedCreated.String)
		}
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 5 {
		t.Errorf("ops memories = %d, want 5", got)
	}
	if got := countRows(t, corpus.SQL(), "memories"); got != 0 {
		t.Errorf("corpus memories = %d, want 0", got)
	}

	second, err := Run(context.Background(), corpus, registry(t), options)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if second.Delta != (Tables{}) {
		t.Errorf("second corpus delta = %+v, want zero", second.Delta)
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 5 {
		t.Errorf("second ops memories = %d, want 5", got)
	}
	if second.Sources[madreSource].MemoriesInserted != 0 {
		t.Errorf("second memories inserted = %d", second.Sources[madreSource].MemoriesInserted)
	}
}

func TestLegacyStoreSkipsFederatedOverlap(t *testing.T) {
	t.Parallel()
	path := seedMadreFixture(t)
	corpus, ops := madreStores(t)
	if err := corpus.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (session_id, source_agent, source_surface, title)
			VALUES (?, 'claude', 'Claude Code', 'already federated')`, madreOverlapSession)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text)
			VALUES (?, 1, 'already here', 'keep this')`, madreOverlapSession)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: path})
	var progress []string
	result, err := Run(context.Background(), corpus, registry(t), Options{
		Roots: roots, Ops: ops, Progress: func(line string) { progress = append(progress, line) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 0 {
		t.Fatalf("errors = %+v", result.ErrorDetails)
	}
	if result.Delta.Sessions != 1 {
		t.Errorf("delta sessions = %d, want 1 (the missing fixture session)", result.Delta.Sessions)
	}
	if result.Sources[madreSource].SessionsSkipped != 1 {
		t.Errorf("overlap sessions skipped = %d, want 1",
			result.Sources[madreSource].SessionsSkipped)
	}
	var reportedOverlap bool
	for _, line := range progress {
		if strings.Contains(line, "sessions_skipped=1 (session_id already present)") {
			reportedOverlap = true
		}
	}
	if !reportedOverlap {
		t.Errorf("progress did not report the overlap: %v", progress)
	}
	if countRows(t, corpus.SQL(), "sessions") != 2 {
		t.Errorf("sessions = %d, want 2", countRows(t, corpus.SQL(), "sessions"))
	}
	var title, surface string
	if err := corpus.SQL().QueryRow(`SELECT COALESCE(title, ''), COALESCE(source_surface, '')
		FROM sessions WHERE session_id = ?`, madreOverlapSession).Scan(&title, &surface); err != nil {
		t.Fatal(err)
	}
	if title != "already federated" || surface != "Claude Code" {
		t.Errorf("overlap session mutated: title=%q surface=%q", title, surface)
	}
	if countRows(t, corpus.SQL(), "exchanges") != 2 {
		t.Errorf("exchanges = %d, want 2 (overlap kept, missing fixture added)",
			countRows(t, corpus.SQL(), "exchanges"))
	}
	for _, table := range []string{"thinking_blocks", "tool_uses"} {
		var got int
		if err := corpus.SQL().QueryRow("SELECT COUNT(*) FROM "+table+" WHERE session_id = ?",
			madreOverlapSession).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("overlap %s = %d, want 0", table, got)
		}
	}
}

func TestLegacyStoreRetriesMemoriesAfterOpsBecomesAvailable(t *testing.T) {
	t.Parallel()
	path := seedMadreFixture(t)
	corpus, ops := madreStores(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()},
		Settings{LegacyStoreDB: path})

	withoutOps, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if withoutOps.Sources[madreSource].Sessions != 2 {
		t.Fatalf("sessions without ops = %+v", withoutOps.Sources[madreSource])
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 0 {
		t.Fatalf("ops memories without ops routing = %d, want 0", got)
	}

	withOps, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	counts := withOps.Sources[madreSource]
	if counts.MemoriesInserted != 5 || counts.SessionsSkipped != 2 {
		t.Fatalf("retry counts = %+v", counts)
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 5 {
		t.Errorf("ops memories after enabling ops = %d, want 5", got)
	}
	if got := countRows(t, corpus.SQL(), "sessions"); got != 2 {
		t.Errorf("corpus sessions after retry = %d, want 2", got)
	}
	if withOps.Delta.Memories != 5 {
		t.Errorf("memory delta after enabling ops = %d, want 5", withOps.Delta.Memories)
	}
}

func TestLegacyStoreReportsCommittedOpsWhenCorpusFails(t *testing.T) {
	t.Parallel()
	path := seedMadreFixture(t)
	corpus, ops := madreStores(t)
	failing := &failOnceDatabase{
		Database: corpus,
		failure:  errors.New("synthetic corpus write failure"),
	}
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()},
		Settings{LegacyStoreDB: path})

	first, err := Run(context.Background(), failing, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	counts := first.Sources[madreSource]
	if first.WriteFailed != 1 || counts.MemoriesInserted != 5 {
		t.Fatalf("failed corpus run = write_failed %d counts %+v", first.WriteFailed, counts)
	}
	if first.Delta.Memories != 5 || first.After.Memories != 5 {
		t.Errorf("failed corpus memory totals = after %d delta %d, want 5/5",
			first.After.Memories, first.Delta.Memories)
	}
	if got := countRows(t, corpus.SQL(), "sessions"); got != 0 {
		t.Errorf("corpus sessions after failed write = %d, want 0", got)
	}

	retry, err := Run(context.Background(), failing, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	retryCounts := retry.Sources[madreSource]
	if retryCounts.MemoriesInserted != 0 || retryCounts.MemoriesUnchanged != 5 {
		t.Errorf("retry memory counts = %+v", retryCounts)
	}
	if retry.Delta.Memories != 0 || retry.Delta.Sessions != 2 {
		t.Errorf("retry delta = %+v, want two sessions and no memories", retry.Delta)
	}
}

func TestLegacyStoreConnectionIsReadOnly(t *testing.T) {
	t.Parallel()
	db, err := openMadreSource(context.Background(), seedMadreFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sessions (session_id) VALUES ('must-not-land')`); err == nil {
		t.Fatal("legacy store connection accepted a write")
	}
}

func TestLegacyStoreRootsAndDetection(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	want := filepath.Join(home, "."+retiredStoreHome(), "roca.db")
	if roots.LegacyStoreDB != want {
		t.Errorf("default = %q, want %q", roots.LegacyStoreDB, want)
	}
	declared := ResolveRoots(Environment{GOOS: "darwin", Home: home},
		Settings{LegacyStoreDB: "/declared/store.db"})
	if declared.LegacyStoreDB != "/declared/store.db" {
		t.Errorf("declared = %q", declared.LegacyStoreDB)
	}
	fromEnv := ResolveRoots(Environment{
		GOOS: "darwin", Home: home,
		Getenv: environmentOf(map[string]string{retiredStoreDBEnv(): "/env/store.db"}),
	}, Settings{})
	if fromEnv.LegacyStoreDB != "/env/store.db" {
		t.Errorf("env = %q", fromEnv.LegacyStoreDB)
	}

	if got := DetectAgents(roots); containsString(got, madreSource) {
		t.Errorf("absent madre was detected: %v", got)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	present := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	if got := DetectAgents(present); !containsString(got, madreSource) {
		t.Errorf("present madre not detected: %v", got)
	}
}

func TestLegacyStoreLabDelta(t *testing.T) {
	dir := os.Getenv("LEGACY_STORE_LAB_DIR")
	if dir == "" {
		t.Skip("set LEGACY_STORE_LAB_DIR to a directory of lab copies to measure the live delta")
	}
	delta := filepath.Join(dir, "roca-delta.db")
	if !isFile(delta) {
		t.Fatalf("lab delta is missing at the configured directory")
	}
	corpus, ops := madreStores(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: delta})
	first, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 {
		t.Fatalf("lab delta errors = %+v", first.ErrorDetails)
	}
	want := Tables{Sessions: 2788, Exchanges: 164857, ThinkingBlocks: 35981, ToolUses: 169492}
	got := Tables{
		Sessions:       countRows(t, corpus.SQL(), "sessions"),
		Exchanges:      countRows(t, corpus.SQL(), "exchanges"),
		ThinkingBlocks: countRows(t, corpus.SQL(), "thinking_blocks"),
		ToolUses:       countRows(t, corpus.SQL(), "tool_uses"),
		Memories:       countRows(t, ops.SQL(), "memories"),
	}
	if got.Sessions != want.Sessions || got.Exchanges != want.Exchanges ||
		got.ThinkingBlocks != want.ThinkingBlocks || got.ToolUses != want.ToolUses {
		t.Errorf("lab corpus = %+v, want %+v", got, want)
	}
	if got.Memories != 1914 {
		t.Errorf("lab ops memories = %d, want 1914", got.Memories)
	}
	var handoffs int
	if err := ops.SQL().QueryRow(`SELECT COUNT(*) FROM memories WHERE layer = 'handoff'`).
		Scan(&handoffs); err != nil {
		t.Fatal(err)
	}
	if handoffs != 1360 {
		t.Errorf("lab ops handoffs = %d, want 1360", handoffs)
	}
	second, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	if second.Delta != (Tables{}) {
		t.Errorf("lab second corpus delta = %+v, want zero", second.Delta)
	}
	if countRows(t, ops.SQL(), "memories") != 1914 {
		t.Errorf("lab second ops memories = %d, want 1914", countRows(t, ops.SQL(), "memories"))
	}
}

func TestLegacyStoreLabSkipsOverlap(t *testing.T) {
	dir := os.Getenv("LEGACY_STORE_LAB_DIR")
	if dir == "" {
		t.Skip("set LEGACY_STORE_LAB_DIR to a directory of lab copies to measure overlap")
	}
	full := filepath.Join(dir, "full.db")
	if !isFile(full) {
		t.Skip("lab full copy is not present")
	}
	ids := legacyStoreSessionSample(t, full, 5)
	corpus, ops := madreStores(t)
	if err := corpus.Write(context.Background(), func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.Exec(`INSERT INTO sessions (session_id, source_agent, source_surface, title)
				VALUES (?, 'claude', 'Claude Code', 'already federated')`, id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: full})
	result, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 0 {
		t.Fatalf("lab overlap errors = %+v", result.ErrorDetails)
	}
	sourceSessions := legacyStoreCount(t, full, "sessions")
	if got := countRows(t, corpus.SQL(), "sessions"); got != sourceSessions {
		t.Errorf("corpus sessions = %d, want %d", got, sourceSessions)
	}
	if result.Delta.Sessions != sourceSessions-len(ids) {
		t.Errorf("added sessions = %d, want %d missing", result.Delta.Sessions, sourceSessions-len(ids))
	}
	for _, id := range ids {
		var title, surface string
		if err := corpus.SQL().QueryRow(`SELECT COALESCE(title, ''), COALESCE(source_surface, '')
			FROM sessions WHERE session_id = ?`, id).Scan(&title, &surface); err != nil {
			t.Fatal(err)
		}
		if title != "already federated" || surface != "Claude Code" {
			t.Errorf("overlap session %s mutated: title=%q surface=%q", id, title, surface)
		}
	}
}

func legacyStoreSessionSample(t *testing.T, path string, n int) []string {
	t.Helper()
	db, err := openForeign(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT session_id FROM sessions WHERE session_id IS NOT NULL AND session_id <> ''
		ORDER BY session_id LIMIT ?`, n)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != n {
		t.Fatalf("session sample = %d, want %d", len(ids), n)
	}
	return ids
}

func legacyStoreCount(t *testing.T, path, table string) int {
	t.Helper()
	db, err := openForeign(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func madreStores(t *testing.T) (*store.DB, *store.DB) {
	t.Helper()
	corpus := rocaDatabase(t)
	opsPath := filepath.Join(t.TempDir(), "roca-ops.db")
	if err := rocaops.ApplySchema(opsPath); err != nil {
		t.Fatalf("ops schema: %v", err)
	}
	ops, err := store.Open(opsPath)
	if err != nil {
		t.Fatalf("open ops: %v", err)
	}
	t.Cleanup(func() { ops.Close() })
	return corpus, ops
}

type failOnceDatabase struct {
	Database
	failure error
}

func (db *failOnceDatabase) Write(ctx context.Context, fn func(*sql.Tx) error) error {
	if db.failure != nil {
		err := db.failure
		db.failure = nil
		return err
	}
	return db.Database.Write(ctx, fn)
}

func madreSessionByID(t *testing.T, records parsers.Records, id string) parsers.Session {
	t.Helper()
	for _, session := range records.Sessions {
		if session.ID == id {
			return session
		}
	}
	t.Fatalf("session %s missing", id)
	return parsers.Session{}
}

func seedMadreFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roca.db")
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `CREATE TABLE sessions (
		session_id TEXT PRIMARY KEY, source_agent TEXT, project TEXT,
		started_at TEXT, ended_at TEXT, duration_minutes INTEGER, title TEXT, metadata TEXT)`)
	exec(t, db, `CREATE TABLE exchanges (
		id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER,
		is_after_compaction INTEGER, human_text TEXT, agent_text TEXT,
		human_timestamp TEXT, agent_timestamp TEXT, response_latency_ms INTEGER)`)
	exec(t, db, `CREATE TABLE tool_uses (
		id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER,
		tool_name TEXT, tool_params_summary TEXT, had_error INTEGER,
		error_message TEXT, initiative_type TEXT)`)
	exec(t, db, `CREATE TABLE thinking_blocks (
		id INTEGER PRIMARY KEY, session_id TEXT, exchange_number INTEGER,
		position_in_session REAL, depth TEXT, caution_ratio REAL, word_count INTEGER,
		is_after_compaction INTEGER, full_text TEXT)`)
	exec(t, db, `CREATE TABLE memories (
		id INTEGER PRIMARY KEY, layer TEXT, content TEXT, metadata TEXT, origin TEXT,
		source_agent TEXT, source_session TEXT, source_sequence INTEGER, project TEXT,
		status TEXT, supersedes INTEGER, created_at TEXT)`)
	exec(t, db, `CREATE TABLE garden_channels (id TEXT PRIMARY KEY, name TEXT)`)
	exec(t, db, `CREATE TABLE proposals (id INTEGER PRIMARY KEY, kind TEXT, summary TEXT)`)
	exec(t, db, `INSERT INTO sessions VALUES (?, 'claude-code', 'demo',
		'2026-08-01 12:00:00', '2026-08-01 12:01:00', 1, 'madre fixture', '{}')`, madreFixtureSession)
	exec(t, db, `INSERT INTO sessions VALUES (?, 'codex', 'demo',
		'2026-08-01 13:00:00', '2026-08-01 13:01:00', 1, 'overlap fixture', '{}')`, madreOverlapSession)
	exec(t, db, `INSERT INTO exchanges VALUES (1, ?, 1, 0, 'count the madre rows', 'two sessions',
		'2026-08-01T12:00:00Z', '2026-08-01T12:00:04Z', 4000)`, madreFixtureSession)
	exec(t, db, `INSERT INTO exchanges VALUES (2, ?, 1, 0, 'already here', 'keep this',
		'2026-08-01T13:00:00Z', '2026-08-01T13:00:02Z', 2000)`, madreOverlapSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (1, ?, 1, 1.0, 'think', 0.1, 2, 0, 'measure first')`,
		madreFixtureSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (2, ?, 1, 1.0, 'think', 0.2, 2, 0, 'do not enrich')`,
		madreOverlapSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (1, ?, 1, 'exec', 'select 1', 0, NULL, 'reactive')`,
		madreFixtureSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (2, ?, 1, 'exec', 'select 2', 0, NULL, 'reactive')`,
		madreOverlapSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (3, ?, 99, 'exec', 'select 99', 0, NULL, 'reactive')`,
		madreFixtureSession)
	exec(t, db, `INSERT INTO memories VALUES (1, 'handoff', ?, '{}', 'agent', 'claude-code', ?, 1, 'demo',
		'pending', NULL, ?)`, madreHandoffContent, madreFixtureSession, madreCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (2, 'feedback', ?, '{}', 'agent', 'claude-code', ?, 2, 'demo',
		'active', NULL, ?)`, madreFeedbackContent, madreFixtureSession, madreCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (3, 'discovery', 'synthetic madre discovery', '{}', 'agent',
		'claude-code', ?, 3, 'demo', 'active', 1, ?)`, madreFixtureSession, madreCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (4, 'handover', 'synthetic legacy handover', '{}', 'agent',
		'claude-code', ?, 4, 'demo', NULL, NULL, NULL)`, madreFixtureSession)
	exec(t, db, `INSERT INTO memories VALUES (5, 'protocol', 'synthetic legacy protocol', '{}', 'agent',
		'claude-code', ?, 5, 'demo', '', NULL, '')`, madreFixtureSession)
	exec(t, db, `INSERT INTO garden_channels VALUES ('garden-1', 'synthetic')`)
	exec(t, db, `INSERT INTO proposals VALUES (1, 'note', 'leave this out')`)
	return path
}
