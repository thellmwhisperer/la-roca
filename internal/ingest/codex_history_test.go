package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// A corpus synced by a build that could not read a fossil's own prompt records
// re-reads it on the parser bump: the recovered prompts land once, carry what
// the fossil's header states, and the run after that reads nothing.
func TestCodexFossilRolloutBackfillsItsOwnPromptsOnce(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	fossil := filepath.Join(roots.CodexSessions, "2025", "11", "18", "rollout-fossil.jsonl")
	world.write(t, fossil, `{"type":"session_meta","timestamp":"2025-11-18T09:42:00Z","payload":{"id":"fossil-codex","cwd":"/synthetic/fossil","model_provider":"fixture-provider"}}
{"session_id":"fossil-codex","ts":1763458940,"text":"inspect the synthetic fossil"}
{"session_id":"fossil-codex","ts":1763459060,"text":"verify the synthetic fossil"}
`)
	db := rocaDatabase(t)
	ctx := context.Background()
	options := Options{Roots: roots}
	if _, err := Run(ctx, db, registry(t), options); err != nil {
		t.Fatalf("fossil ingest: %v", err)
	}
	if got := countRows(t, db.SQL(), "exchanges WHERE session_id = 'fossil-codex'"); got != 2 {
		t.Fatalf("recovered exchanges = %d, want both fossil prompts", got)
	}

	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE ingest_file_state SET fingerprint = replace(fingerprint, '-v8', '-v7')`)
		return err
	}); err != nil {
		t.Fatalf("age the watermark: %v", err)
	}
	replay, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("fossil backfill: %v", err)
	}
	if replay.Delta != (Tables{}) || countRows(t, db.SQL(),
		"exchanges WHERE session_id = 'fossil-codex'") != 2 {
		t.Fatalf("the backfill duplicated the fossil: delta=%+v", replay.Delta)
	}

	var project, cwd, humanTS string
	err = db.SQL().QueryRow(`SELECT COALESCE(s.project, ''),
		COALESCE(json_extract(s.metadata, '$.cwd'), ''), e.human_timestamp
		FROM sessions s JOIN exchanges e ON e.session_id = s.session_id
		WHERE s.session_id = 'fossil-codex' AND e.exchange_number = 2`).
		Scan(&project, &cwd, &humanTS)
	if err != nil {
		t.Fatal(err)
	}
	if project == "" || cwd != "/synthetic/fossil" || humanTS != "2025-11-18T09:44:20Z" {
		t.Errorf("fossil exchange = project %q cwd %q at %q", project, cwd, humanTS)
	}

	settled, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("idempotent ingest: %v", err)
	}
	if settled.FilesRead != 0 || settled.Delta != (Tables{}) {
		t.Errorf("idempotent ingest read or wrote records: files=%d delta=%+v",
			settled.FilesRead, settled.Delta)
	}
}

func TestCodexHistoryBackfillsMetadataOnlySessionsOnce(t *testing.T) {
	world := newWorld(t)
	roots := world.roots()
	legacyPath := filepath.Join(roots.CodexSessions, "2025", "11", "17", "rollout-legacy.jsonl")
	world.write(t, legacyPath, `{"type":"session_meta","timestamp":"2025-11-17T09:42:20Z","payload":{"id":"legacy-codex","cwd":"/synthetic/archive","timestamp":"2025-11-17T09:42:20Z","cli_version":"0.58.0","model_provider":"fixture-provider"}}`+"\n")
	state := openSynthetic(t, roots.CodexStateDB)
	exec(t, state, `CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT,
		model_provider TEXT, model TEXT, tokens_used INTEGER, cwd TEXT)`)
	exec(t, state, `INSERT INTO threads VALUES
		('legacy-codex', ?, 'fixture-provider', 'fixture-legacy-model', 321, '/synthetic/archive')`, legacyPath)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	db := rocaDatabase(t)
	ctx := context.Background()
	options := Options{Roots: roots}
	first, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("metadata-only ingest: %v", err)
	}
	if got := countRows(t, db.SQL(), "sessions WHERE session_id = 'legacy-codex'"); got != 1 {
		t.Fatalf("legacy sessions = %d, want 1", got)
	}
	if got := countRows(t, db.SQL(), "exchanges WHERE session_id = 'legacy-codex'"); got != 0 {
		t.Fatalf("legacy exchanges before backfill = %d, want 0", got)
	}
	if first.Scanned["codex_history_files"] != 0 {
		t.Fatalf("history files before fixture = %d, want 0", first.Scanned["codex_history_files"])
	}

	world.write(t, filepath.Join(roots.CodexRoot, "history.jsonl"), `
{"session_id":"legacy-codex","ts":1763372540,"text":"inspect the synthetic archive"}
not json
{"type":"session_meta","session_id":"noise","ts":1763372570,"text":"not history"}
{"session_id":"codex-thread-1","ts":1785574797,"text":"start the sixth fixture"}
{"session_id":"codex-thread-1","ts":1785574680,"text":"start the sixth fixture"}
{"session_id":"codex-thread-1","ts":1785574801,"text":"this richer rollout already landed"}
{"session_id":"legacy-codex","ts":1763372660,"text":"verify the synthetic archive"}
{"session_id":"","ts":1763372700,"text":"orphaned input"}
`)
	second, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("history backfill: %v", err)
	}
	if second.Scanned["codex_history_files"] != 1 || second.Delta.Sessions != 0 ||
		second.Delta.Exchanges != 4 {
		t.Fatalf("history backfill = scanned:%d delta:%+v", second.Scanned["codex_history_files"], second.Delta)
	}
	if got := countRows(t, db.SQL(), "exchanges WHERE session_id = 'codex-thread-1'"); got != 3 {
		t.Fatalf("richer rollout exchanges = %d, want unmatched and out-of-window history prompts", got)
	}
	discardCounts := map[string]int{}
	for _, category := range second.DiscardSummary {
		discardCounts[category.Reason] = category.Count
	}
	if discardCounts["invalid Codex history JSON"] != 1 ||
		discardCounts["invalid Codex history record"] != 1 ||
		discardCounts["codex runtime record not ingested from history"] != 1 {
		t.Fatalf("history discard summary = %+v", second.DiscardSummary)
	}

	var human, agent, humanTS, model, provider string
	var tokensIn, tokensOut sql.NullInt64
	err = db.SQL().QueryRow(`SELECT human_text, COALESCE(agent_text, ''), human_timestamp,
		COALESCE(model, ''), COALESCE(provider, ''), tokens_in, tokens_out
		FROM exchanges WHERE session_id = 'legacy-codex' AND exchange_number = 2`).
		Scan(&human, &agent, &humanTS, &model, &provider, &tokensIn, &tokensOut)
	if err != nil {
		t.Fatal(err)
	}
	if human != "verify the synthetic archive" || agent != "" ||
		humanTS != "2025-11-17T09:44:20Z" || model != "fixture-legacy-model" ||
		provider != "fixture-provider" || tokensIn.Valid || tokensOut.Valid {
		t.Errorf("recovered exchange = %q/%q at %q model=%q provider=%q tokens=%v/%v",
			human, agent, humanTS, model, provider, tokensIn, tokensOut)
	}

	world.write(t, filepath.Join(roots.CodexRoot, "history.jsonl"), `
{"session_id":"legacy-codex","ts":1763372540,"text":"inspect the synthetic archive"}
{"session_id":"legacy-codex","ts":1763372660,"text":"verify the synthetic archive"}
{"session_id":"legacy-codex","ts":1763372780,"text":"report the synthetic archive"}
`)
	third, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("grown history ingest: %v", err)
	}
	if third.Delta.Exchanges != 1 || countRows(t, db.SQL(),
		"exchanges WHERE session_id = 'legacy-codex'") != 3 {
		t.Fatalf("grown history delta = %+v", third.Delta)
	}

	world.write(t, legacyPath, `
{"type":"session_meta","timestamp":"2025-11-17T09:42:20Z","payload":{"id":"legacy-codex","cwd":"/synthetic/archive","timestamp":"2025-11-17T09:42:20Z","model_provider":"fixture-provider"}}
{"type":"turn_context","timestamp":"2025-11-17T09:42:20Z","payload":{"model":"fixture-legacy-model"}}
{"type":"event_msg","timestamp":"2025-11-17T09:42:24.234Z","payload":{"type":"user_message","message":"inspect the synthetic archive"}}
{"type":"event_msg","timestamp":"2025-11-17T09:43:00Z","payload":{"type":"task_complete","last_agent_message":"archive inspected"}}
{"type":"event_msg","timestamp":"2025-11-17T09:44:24.234Z","payload":{"type":"user_message","message":"verify the synthetic archive"}}
{"type":"event_msg","timestamp":"2025-11-17T09:45:00Z","payload":{"type":"task_complete","last_agent_message":"archive verified"}}
`)
	fourth, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("richer rollout ingest: %v", err)
	}
	if fourth.Delta.Exchanges != 0 || countRows(t, db.SQL(),
		"exchanges WHERE session_id = 'legacy-codex'") != 3 {
		t.Fatalf("richer rollout duplicated fallback prompts: delta=%+v", fourth.Delta)
	}
	var answer, answeredAt string
	err = db.SQL().QueryRow(`SELECT agent_text, agent_timestamp FROM exchanges
		WHERE session_id = 'legacy-codex' AND exchange_number = 1`).Scan(&answer, &answeredAt)
	if err != nil {
		t.Fatal(err)
	}
	if answer != "archive inspected" || answeredAt != "2025-11-17T09:43:00Z" {
		t.Errorf("enriched fallback = %q at %q", answer, answeredAt)
	}

	fifth, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatalf("idempotent ingest: %v", err)
	}
	if fifth.FilesRead != 0 || fifth.Delta != (Tables{}) {
		t.Errorf("idempotent ingest read or wrote records: files=%d delta=%+v", fifth.FilesRead, fifth.Delta)
	}
}
