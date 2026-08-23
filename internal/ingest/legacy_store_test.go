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
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const (
	legacyFixtureSession          = "legacy-fixture-session"
	legacyOverlapSession          = "legacy-overlap-session"
	legacyFederatedPayloadSession = "federated-exact-payload"
	legacyHandoffContent          = "synthetic legacy handoff for the next agent"
	legacyFeedbackContent         = "synthetic legacy feedback about the ingest route"
	legacyCreatedAt               = "2026-08-01 12:00:00"
)

func TestLegacyStoreIngest(t *testing.T) {
	t.Parallel()
	path := seedLegacyStoreFixture(t)

	records, discards, err := ReadLegacyStore(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadLegacyStore: %v", err)
	}
	if records.Seen.Sessions != 4 {
		t.Fatalf("seen sessions = %d, want 4", records.Seen.Sessions)
	}
	if len(records.Sessions) != 4 {
		t.Fatalf("sessions = %d, want 4", len(records.Sessions))
	}
	session := legacyStoreSessionByID(t, records, legacyFixtureSession)
	if session.SourceAgent != "claude-code" {
		t.Errorf("session source_agent = %q, want claude-code", session.SourceAgent)
	}
	if session.SourceSurface != ingestprovenance.LegacyStore {
		t.Errorf("session source_surface = %q, want %q", session.SourceSurface, ingestprovenance.LegacyStore)
	}
	if session.Metadata["source_note"] != "session kept" {
		t.Errorf("session metadata = %+v", session.Metadata)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(session.Exchanges))
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "count the legacy rows" || exchange.AgentText != "two sessions" {
		t.Errorf("exchange text = %q / %q", exchange.HumanText, exchange.AgentText)
	}
	if exchange.Provenance.Model != "claude-opus-4" || exchange.Provenance.Provider != "anthropic" ||
		exchange.Provenance.TokensIn == nil || *exchange.Provenance.TokensIn != 11 ||
		exchange.Provenance.TokensOut == nil || *exchange.Provenance.TokensOut != 7 ||
		exchange.Provenance.TokensReasoning == nil || *exchange.Provenance.TokensReasoning != 3 ||
		exchange.Provenance.CostUSD == nil || *exchange.Provenance.CostUSD != 0.25 {
		t.Errorf("exchange provenance = %+v", exchange.Provenance)
	}
	if len(exchange.Thinking) != 1 || exchange.Thinking[0].Text != "measure first" {
		t.Errorf("thinking = %+v", exchange.Thinking)
	}
	if len(exchange.Tools) != 1 || exchange.Tools[0].Name != "exec" {
		t.Errorf("tools = %+v", exchange.Tools)
	}
	if len(session.Thinking) != 3 || session.Thinking[0].Text != "unmatched duplicate" ||
		session.Thinking[1].Text != "unmatched duplicate" ||
		session.Thinking[2].Text != "unmatched third" ||
		session.Thinking[0].Position == session.Thinking[1].Position {
		t.Errorf("unmatched thinking = %+v, want deterministic source order", session.Thinking)
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
		if memory.Source != legacyStoreSource || memory.FilePath == "" {
			t.Errorf("memory %q identity = %q %q", memory.Layer, memory.Source, memory.FilePath)
		}
		if memory.Layer != "handover" && memory.Layer != "protocol" &&
			memory.CreatedAt != legacyCreatedAt {
			t.Errorf("memory %q created_at = %q", memory.Layer, memory.CreatedAt)
		}
	}
	handoff, ok := layers["handoff"]
	if !ok {
		t.Fatal("handoff memory missing")
	}
	if handoff.Content != legacyHandoffContent || handoff.Status != "pending" {
		t.Errorf("handoff = %+v", handoff)
	}
	if handoff.SourceModel != "legacy-memory-model" || handoff.Metadata["source_note"] != "memory kept" ||
		handoff.Metadata["file_path"] != legacyStoreMemoryFile+"1" {
		t.Errorf("handoff provenance = model %q metadata %+v", handoff.SourceModel, handoff.Metadata)
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
	reasons := map[string]int{}
	for _, discard := range records.Discards {
		reasons[discard.Reason]++
	}
	for _, reason := range []string{
		legacyStoreMissingToolExchangeReason,
		legacyStoreMissingExchangeSessionReason,
		legacyStoreMissingToolSessionReason,
		legacyStoreMissingThinkingSessionReason,
		legacyStoreEmptyMemoryReason,
		"legacy store layer statistics",
	} {
		if reasons[reason] != 1 {
			t.Errorf("discard reason %q = %d, want 1", reason, reasons[reason])
		}
	}
	for _, discard := range records.Discards {
		wantByDesign := discard.Reason == legacyStoreEmptyMemoryReason ||
			discard.Reason == "legacy store layer statistics" ||
			discard.Reason == "legacy store garden records" ||
			discard.Reason == "legacy store proposals"
		if discard.ByDesign != wantByDesign {
			t.Errorf("discard %q by_design = %t, want %t", discard.Reason, discard.ByDesign, wantByDesign)
		}
	}
	for _, id := range []string{"legacy-store:empty-session:101", "legacy-store:empty-session:102"} {
		if got := legacyStoreSessionByID(t, records, id); got.ID != id {
			t.Errorf("fallback session = %q, want %q", got.ID, id)
		}
	}

	corpus, ops := legacyStoreStores(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: path})
	options := Options{Roots: roots, Ops: ops}
	first, err := Run(context.Background(), corpus, registry(t), options)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if first.Errors != 0 {
		t.Fatalf("first errors = %+v", first.ErrorDetails)
	}
	counts, ok := first.Sources[legacyStoreSource]
	if !ok {
		t.Fatalf("source %q missing: %v", legacyStoreSource, SortedSources(first.Sources))
	}
	if counts.Sessions != 4 || counts.Exchanges != 2 || counts.ThinkingBlocks != 5 ||
		counts.ToolUses != 2 || counts.MemoriesInserted != 5 {
		t.Fatalf("first source counts = %+v", counts)
	}
	if first.Delta.Sessions != 4 || first.Delta.Exchanges != 2 || first.Delta.Memories != 5 {
		t.Fatalf("first aggregate delta = %+v", first.Delta)
	}
	if first.RecordsDiscarded != 4 {
		t.Errorf("first discarded records = %d, want 4 invalid child references", first.RecordsDiscarded)
	}

	var surface, agent, sessionNote, nestedNote string
	var sourceExchange int
	if err := corpus.SQL().QueryRow(`
		SELECT source_surface, source_agent, json_extract(metadata, '$.source_note'),
		       json_extract(metadata, '$."legacy-store".source_note'),
		       json_extract(metadata, '$."legacy-store".source_exchange_ids."1"')
		FROM sessions WHERE session_id = ?`, legacyFixtureSession).
		Scan(&surface, &agent, &sessionNote, &nestedNote, &sourceExchange); err != nil {
		t.Fatal(err)
	}
	if surface != ingestprovenance.LegacyStore || agent != "claude-code" ||
		sessionNote != "session kept" || nestedNote != "nested kept" || sourceExchange != 1 {
		t.Errorf("landed session provenance = %q / %q metadata %q / %q exchange %d",
			surface, agent, sessionNote, nestedNote, sourceExchange)
	}
	var model, provider string
	var tokensIn, tokensOut, tokensReasoning int
	var cost float64
	if err := corpus.SQL().QueryRow(`
		SELECT model, provider, tokens_in, tokens_out, tokens_reasoning, cost_usd
		FROM exchanges WHERE session_id = ? AND exchange_number = 1`, legacyFixtureSession).
		Scan(&model, &provider, &tokensIn, &tokensOut, &tokensReasoning, &cost); err != nil {
		t.Fatal(err)
	}
	if model != "claude-opus-4" || provider != "anthropic" || tokensIn != 11 ||
		tokensOut != 7 || tokensReasoning != 3 || cost != 0.25 {
		t.Errorf("landed exchange provenance = %q/%q %d/%d/%d %.2f",
			model, provider, tokensIn, tokensOut, tokensReasoning, cost)
	}
	var unmatched, distinctPositions, assignedExchanges int
	if err := corpus.SQL().QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT position_in_session), COUNT(exchange_number)
		FROM thinking_blocks WHERE session_id = ? AND full_text = 'unmatched duplicate'`,
		legacyFixtureSession).Scan(&unmatched, &distinctPositions, &assignedExchanges); err != nil {
		t.Fatal(err)
	}
	if unmatched != 2 || distinctPositions != 2 || assignedExchanges != 0 {
		t.Errorf("landed unmatched thinking = rows %d positions %d assigned exchanges %d",
			unmatched, distinctPositions, assignedExchanges)
	}

	var layer, status, created, sourceModel, memoryNote string
	var expires sql.NullString
	if err := ops.SQL().QueryRow(`
		SELECT layer, status, created_at, expires_at, source_model,
		       json_extract(metadata, '$.source_note')
		FROM memories WHERE content = ?`, legacyHandoffContent).
		Scan(&layer, &status, &created, &expires, &sourceModel, &memoryNote); err != nil {
		t.Fatal(err)
	}
	if layer != "handoff" {
		t.Errorf("handoff landed in layer %q", layer)
	}
	if status != "pending" || created != legacyCreatedAt {
		t.Errorf("handoff status/created_at = %q / %q", status, created)
	}
	if expires.Valid {
		t.Errorf("handoff expires_at = %q, want NULL", expires.String)
	}
	if sourceModel != "legacy-memory-model" || memoryNote != "memory kept" {
		t.Errorf("handoff source metadata = model %q note %q", sourceModel, memoryNote)
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
	var sourceMemoryID, sourceSupersedes int64
	var landedSupersedes sql.NullInt64
	if err := ops.SQL().QueryRow(`
		SELECT json_extract(metadata, '$.legacy_memory_id'),
		       json_extract(metadata, '$.legacy_supersedes'), supersedes
		FROM memories WHERE content = 'synthetic legacy discovery'`).
		Scan(&sourceMemoryID, &sourceSupersedes, &landedSupersedes); err != nil {
		t.Fatal(err)
	}
	if sourceMemoryID != 3 || sourceSupersedes != 1 || !landedSupersedes.Valid {
		t.Errorf("legacy supersedes metadata = source %d supersedes %d landed %+v",
			sourceMemoryID, sourceSupersedes, landedSupersedes)
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
	if second.Sources[legacyStoreSource].MemoriesInserted != 0 {
		t.Errorf("second memories inserted = %d", second.Sources[legacyStoreSource].MemoriesInserted)
	}
}

func TestLegacyStoreSkipsFederatedOverlap(t *testing.T) {
	t.Parallel()
	path := seedLegacyStoreFixture(t)
	corpus, ops := legacyStoreStores(t)
	if err := corpus.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions (session_id, source_agent, source_surface, title)
			VALUES (?, 'claude', 'Claude Code', 'already federated')`, legacyOverlapSession)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text)
			VALUES (?, 1, 'already here', 'keep this')`, legacyOverlapSession)
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
	if result.Delta.Sessions != 3 {
		t.Errorf("delta sessions = %d, want 3 missing fixture sessions", result.Delta.Sessions)
	}
	if result.Sources[legacyStoreSource].SessionsSkipped != 1 {
		t.Errorf("overlap sessions skipped = %d, want 1",
			result.Sources[legacyStoreSource].SessionsSkipped)
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
	if countRows(t, corpus.SQL(), "sessions") != 4 {
		t.Errorf("sessions = %d, want 4", countRows(t, corpus.SQL(), "sessions"))
	}
	var title, surface string
	if err := corpus.SQL().QueryRow(`SELECT COALESCE(title, ''), COALESCE(source_surface, '')
		FROM sessions WHERE session_id = ?`, legacyOverlapSession).Scan(&title, &surface); err != nil {
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
			legacyOverlapSession).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("overlap %s = %d, want 0", table, got)
		}
	}
}

func TestLegacyStoreSkipsExactPayloadOverlapAndContinues(t *testing.T) {
	t.Parallel()
	path := seedLegacyStoreFixture(t)
	corpus, ops := legacyStoreStores(t)
	if err := exactdedup.EnsureGuards(context.Background(), corpus.SQL()); err != nil {
		t.Fatal(err)
	}
	if err := corpus.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions
			(session_id, source_agent, source_surface, project, started_at, ended_at,
			 duration_minutes, title, metadata)
			VALUES (?, 'codex', ?, 'demo', '2026-08-01 13:00:00', '2026-08-01 13:01:00',
			        1, 'overlap fixture', '{}')`,
			legacyFederatedPayloadSession, ingestprovenance.LegacyStore)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()}, Settings{LegacyStoreDB: path})
	var progress []string
	options := Options{Roots: roots, Ops: ops, Progress: func(line string) { progress = append(progress, line) }}
	result, err := Run(context.Background(), corpus, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Errors != 0 || result.WriteFailed != 0 {
		t.Fatalf("exact-payload overlap aborted the source: errors=%+v write_failed=%d",
			result.ErrorDetails, result.WriteFailed)
	}
	if result.Delta.Sessions != 3 {
		t.Errorf("delta sessions = %d, want 3 new sessions", result.Delta.Sessions)
	}
	if result.Sources[legacyStoreSource].SessionsSkipped != 1 {
		t.Errorf("exact-payload sessions skipped = %d, want 1",
			result.Sources[legacyStoreSource].SessionsSkipped)
	}
	if result.Sources[legacyStoreSource].Sessions != 3 {
		t.Errorf("ingested sessions = %d, want 3", result.Sources[legacyStoreSource].Sessions)
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
	if countRows(t, corpus.SQL(), "sessions") != 4 {
		t.Errorf("sessions = %d, want 4 (1 federated + 3 new)", countRows(t, corpus.SQL(), "sessions"))
	}
	var title, surface string
	if err := corpus.SQL().QueryRow(`SELECT COALESCE(title, ''), COALESCE(source_surface, '')
		FROM sessions WHERE session_id = ?`, legacyFederatedPayloadSession).Scan(&title, &surface); err != nil {
		t.Fatal(err)
	}
	if title != "overlap fixture" || surface != ingestprovenance.LegacyStore {
		t.Errorf("federated payload session mutated: title=%q surface=%q", title, surface)
	}
	var overlapLanded int
	if err := corpus.SQL().QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = ?`,
		legacyOverlapSession).Scan(&overlapLanded); err != nil {
		t.Fatal(err)
	}
	if overlapLanded != 0 {
		t.Errorf("exact-payload overlap inserted a second session row")
	}
	if countRows(t, corpus.SQL(), "exchanges") != 1 {
		t.Errorf("exchanges = %d, want 1 from the new sessions", countRows(t, corpus.SQL(), "exchanges"))
	}

	second, err := Run(context.Background(), corpus, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Errors != 0 || second.Delta != (Tables{}) {
		t.Errorf("second run errors=%+v delta=%+v, want zero", second.ErrorDetails, second.Delta)
	}
}

func TestLegacyStoreRetriesMemoriesAfterOpsBecomesAvailable(t *testing.T) {
	t.Parallel()
	path := seedLegacyStoreFixture(t)
	corpus, ops := legacyStoreStores(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: t.TempDir()},
		Settings{LegacyStoreDB: path})

	withoutOps, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	if withoutOps.Sources[legacyStoreSource].Sessions != 4 {
		t.Fatalf("sessions without ops = %+v", withoutOps.Sources[legacyStoreSource])
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 0 {
		t.Fatalf("ops memories without ops routing = %d, want 0", got)
	}

	withOps, err := Run(context.Background(), corpus, registry(t), Options{Roots: roots, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	counts := withOps.Sources[legacyStoreSource]
	if counts.MemoriesInserted != 5 || counts.SessionsSkipped != 4 {
		t.Fatalf("retry counts = %+v", counts)
	}
	if got := countRows(t, ops.SQL(), "memories"); got != 5 {
		t.Errorf("ops memories after enabling ops = %d, want 5", got)
	}
	if got := countRows(t, corpus.SQL(), "sessions"); got != 4 {
		t.Errorf("corpus sessions after retry = %d, want 4", got)
	}
	if withOps.Delta.Memories != 5 {
		t.Errorf("memory delta after enabling ops = %d, want 5", withOps.Delta.Memories)
	}
}

func TestLegacyStoreReportsCommittedOpsWhenCorpusFails(t *testing.T) {
	t.Parallel()
	path := seedLegacyStoreFixture(t)
	corpus, ops := legacyStoreStores(t)
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
	counts := first.Sources[legacyStoreSource]
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
	retryCounts := retry.Sources[legacyStoreSource]
	if retryCounts.MemoriesInserted != 0 || retryCounts.MemoriesUnchanged != 5 {
		t.Errorf("retry memory counts = %+v", retryCounts)
	}
	if retry.Delta.Memories != 0 || retry.Delta.Sessions != 4 {
		t.Errorf("retry delta = %+v, want four sessions and no memories", retry.Delta)
	}
}

func TestLegacyStoreConnectionIsReadOnly(t *testing.T) {
	t.Parallel()
	db, err := openLegacyStoreSource(context.Background(), seedLegacyStoreFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO sessions (session_id) VALUES ('must-not-land')`); err == nil {
		t.Fatal("legacy store connection accepted a write")
	}
}

func TestLegacyStoreAcceptsMeasuredSchemaWithoutOptionalProvenance(t *testing.T) {
	t.Parallel()
	records, _, err := ReadLegacyStore(context.Background(), seedLegacyStoreMeasuredFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	session := legacyStoreSessionByID(t, records, legacyFixtureSession)
	if !session.Exchanges[0].Provenance.Empty() {
		t.Errorf("absent exchange provenance = %+v, want empty", session.Exchanges[0].Provenance)
	}
	for _, memory := range records.Memories {
		if memory.SourceModel != "" {
			t.Errorf("absent memory source_model = %q", memory.SourceModel)
		}
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

	if got := DetectAgents(roots); containsString(got, legacyStoreSource) {
		t.Errorf("absent legacy store was detected: %v", got)
	}
	if err := os.MkdirAll(filepath.Dir(want), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	present := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
	if got := DetectAgents(present); !containsString(got, legacyStoreSource) {
		t.Errorf("present legacy store not detected: %v", got)
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
	corpus, ops := legacyStoreStores(t)
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
	corpus, ops := legacyStoreStores(t)
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

func legacyStoreStores(t *testing.T) (*store.DB, *store.DB) {
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

func legacyStoreSessionByID(t *testing.T, records parsers.Records, id string) parsers.Session {
	t.Helper()
	for _, session := range records.Sessions {
		if session.ID == id {
			return session
		}
	}
	t.Fatalf("session %s missing", id)
	return parsers.Session{}
}

func seedLegacyStoreFixture(t *testing.T) string {
	return seedLegacyStoreFixtureWithOptionalProvenance(t, true)
}

func seedLegacyStoreMeasuredFixture(t *testing.T) string {
	return seedLegacyStoreFixtureWithOptionalProvenance(t, false)
}

func seedLegacyStoreFixtureWithOptionalProvenance(t *testing.T, optional bool) string {
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
	exec(t, db, `CREATE TABLE layer_stats (layer TEXT PRIMARY KEY, count INTEGER)`)
	exec(t, db, `INSERT INTO sessions VALUES (?, 'claude-code', 'demo',
		'2026-08-01 12:00:00', '2026-08-01 12:01:00', 1, 'legacy fixture',
		'{"source_note":"session kept","legacy-store":{"source_note":"nested kept"}}')`, legacyFixtureSession)
	exec(t, db, `INSERT INTO sessions VALUES (?, 'codex', 'demo',
		'2026-08-01 13:00:00', '2026-08-01 13:01:00', 1, 'overlap fixture', '{}')`, legacyOverlapSession)
	exec(t, db, `INSERT INTO sessions(rowid, session_id, source_agent, project, started_at, title)
		VALUES (101, NULL, 'codex', 'demo', '2026-08-01 14:00:00', 'empty session one')`)
	exec(t, db, `INSERT INTO sessions(rowid, session_id, source_agent, project, started_at, title)
		VALUES (102, NULL, 'codex', 'demo', '2026-08-01 15:00:00', 'empty session two')`)
	exec(t, db, `INSERT INTO exchanges VALUES (1, ?, 1, 0, 'count the legacy rows', 'two sessions',
		'2026-08-01T12:00:00Z', '2026-08-01T12:00:04Z', 4000)`, legacyFixtureSession)
	exec(t, db, `INSERT INTO exchanges VALUES (2, ?, 1, 0, 'already here', 'keep this',
		'2026-08-01T13:00:00Z', '2026-08-01T13:00:02Z', 2000)`, legacyOverlapSession)
	exec(t, db, `INSERT INTO exchanges VALUES (3, 'missing-session', 1, 0, 'orphan', 'orphan',
		'2026-08-01T16:00:00Z', '2026-08-01T16:00:01Z', 1000)`)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (1, ?, 1, 1.0, 'think', 0.1, 2, 0, 'measure first')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (2, ?, 1, 1.0, 'think', 0.2, 2, 0, 'do not enrich')`,
		legacyOverlapSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (3, 'missing-session', 1, 1.0, 'think', 0.2, 1, 0, 'orphan')`)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (4, ?, 2, 2.0, 'think', 0.3, 2, 0, 'unmatched duplicate')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (5, ?, 2, 2.0, 'think', 0.3, 2, 0, 'unmatched duplicate')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO thinking_blocks VALUES (6, ?, 3, 1.0, 'think', 0.4, 2, 0, 'unmatched third')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (1, ?, 1, 'exec', 'select 1', 0, NULL, 'reactive')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (2, ?, 1, 'exec', 'select 2', 0, NULL, 'reactive')`,
		legacyOverlapSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (3, ?, 99, 'exec', 'select 99', 0, NULL, 'reactive')`,
		legacyFixtureSession)
	exec(t, db, `INSERT INTO tool_uses VALUES (4, 'missing-session', 1, 'exec', 'orphan', 0, NULL, 'reactive')`)
	exec(t, db, `INSERT INTO memories VALUES (1, 'handoff', ?, '{"source_note":"memory kept","file_path":"source path"}', 'agent', 'claude-code', ?, 1, 'demo',
		'pending', NULL, ?)`, legacyHandoffContent, legacyFixtureSession, legacyCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (2, 'feedback', ?, '{}', 'agent', 'claude-code', ?, 2, 'demo',
		'active', NULL, ?)`, legacyFeedbackContent, legacyFixtureSession, legacyCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (3, 'discovery', 'synthetic legacy discovery', '{}', 'agent',
		'claude-code', ?, 3, 'demo', 'active', 1, ?)`, legacyFixtureSession, legacyCreatedAt)
	exec(t, db, `INSERT INTO memories VALUES (4, 'handover', 'synthetic legacy handover', '{}', 'agent',
		'claude-code', ?, 4, 'demo', NULL, NULL, NULL)`, legacyFixtureSession)
	exec(t, db, `INSERT INTO memories VALUES (5, 'protocol', 'synthetic legacy protocol', '{}', 'agent',
		'claude-code', ?, 5, 'demo', '', NULL, '')`, legacyFixtureSession)
	exec(t, db, `INSERT INTO memories VALUES (6, 'question', '', '{}', 'agent', 'codex', NULL, 6, 'demo',
		'active', NULL, '2026-08-01 16:00:00')`)
	exec(t, db, `INSERT INTO garden_channels VALUES ('garden-1', 'synthetic')`)
	exec(t, db, `INSERT INTO proposals VALUES (1, 'note', 'leave this out')`)
	exec(t, db, `INSERT INTO layer_stats VALUES ('handoff', 1)`)
	if optional {
		for _, statement := range []string{
			`ALTER TABLE exchanges ADD COLUMN model TEXT`,
			`ALTER TABLE exchanges ADD COLUMN provider TEXT`,
			`ALTER TABLE exchanges ADD COLUMN tokens_in INTEGER`,
			`ALTER TABLE exchanges ADD COLUMN tokens_out INTEGER`,
			`ALTER TABLE exchanges ADD COLUMN tokens_reasoning INTEGER`,
			`ALTER TABLE exchanges ADD COLUMN cost_usd REAL`,
			`ALTER TABLE memories ADD COLUMN source_model TEXT`,
			`UPDATE exchanges SET model = 'claude-opus-4', provider = 'anthropic',
			 tokens_in = 11, tokens_out = 7, tokens_reasoning = 3, cost_usd = 0.25 WHERE id = 1`,
			`UPDATE memories SET source_model = 'legacy-memory-model' WHERE id = 1`,
		} {
			exec(t, db, statement)
		}
	}
	return path
}
