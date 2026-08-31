package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func syntheticZCodeContent(t *testing.T) string {
	return syntheticDatabaseFixture(t, "db.sqlite", "zcode-content.sql")
}

func TestZCodeConvertsVisibleMessagesWithModelsAndContent(t *testing.T) {
	path := syntheticZCodeContent(t)
	records := readSyntheticDatabase(t, path, ReadZCode)
	if records.Seen.Sessions != 1 || records.Seen.Messages != 8 {
		t.Fatalf("seen = %+v, want one session and eight messages", records.Seen)
	}
	coverage := records.MessageCoverage
	if coverage == nil || coverage.Seen != 8 || coverage.Converted != 3 ||
		coverage.Skipped["synthetic or hidden message"] != 1 ||
		coverage.Skipped["timeline telemetry"] != 1 ||
		coverage.Skipped["message declares no model"] != 2 ||
		coverage.Skipped["assistant message still being written"] != 1 {
		t.Fatalf("message coverage = %+v", coverage)
	}
	if records.Deferred != 1 {
		t.Fatalf("deferred = %d, want one live assistant message", records.Deferred)
	}

	session := records.Sessions[0]
	if session.ID != "zcode:synthetic-zcode-session" || session.SourceAgent != "zcode" ||
		session.Title != "Map the invented beacon" || session.Project != "opal" ||
		session.StartedAt != "2026-08-01T00:00:00Z" ||
		session.EndedAt != "2026-08-01T00:01:00Z" {
		t.Fatalf("session = %+v", session)
	}
	if session.Metadata["version"] != "0.16.5" ||
		session.Metadata["latest_schema_migration"] != "0018_session_input_failed_status" {
		t.Fatalf("session metadata = %+v", session.Metadata)
	}
	if len(session.Exchanges) != 3 {
		t.Fatalf("exchanges = %d, want one per converted message", len(session.Exchanges))
	}
	for _, exchange := range session.Exchanges {
		if exchange.Provenance.Model == "" {
			t.Fatalf("exchange %q has no model: %+v", exchange.SourceID, exchange)
		}
	}

	user, rich, toolOnly := session.Exchanges[0], session.Exchanges[1], session.Exchanges[2]
	if user.SourceID != "user-visible" || user.HumanText != "map the synthetic beacon" ||
		user.AgentText != "" || user.Provenance.Model != "synthetic-model-a" ||
		user.Provenance.Provider != "synthetic-provider" ||
		user.HumanTimestamp != "2026-08-01T00:00:00Z" {
		t.Errorf("user exchange = %+v", user)
	}
	if rich.SourceID != "assistant-rich" || rich.HumanText != "" ||
		rich.AgentText != "the synthetic beacon is mapped" ||
		rich.Provenance.Model != "synthetic-model-a" || rich.Provenance.Provider != "synthetic-provider" ||
		rich.Provenance.TokensIn == nil || *rich.Provenance.TokensIn != 15 ||
		rich.Provenance.TokensOut == nil || *rich.Provenance.TokensOut != 5 ||
		rich.Provenance.TokensReasoning == nil || *rich.Provenance.TokensReasoning != 3 ||
		rich.Provenance.CostUSD == nil || *rich.Provenance.CostUSD != 0.25 ||
		rich.AgentTimestamp != "2026-08-01T00:00:05Z" {
		t.Errorf("rich assistant exchange = %+v", rich)
	}
	if len(rich.Thinking) != 1 || rich.Thinking[0].Text != "follow the invented coordinates" {
		t.Errorf("thinking = %+v", rich.Thinking)
	}
	if len(rich.Tools) != 1 || rich.Tools[0].Name != "Bash" ||
		rich.Tools[0].ParamsSummary != `{"command":"find synthetic beacon"}` || rich.Tools[0].HadError {
		t.Errorf("completed tool = %+v", rich.Tools)
	}
	if toolOnly.SourceID != "assistant-tool-only" || toolOnly.AgentText != "[tool Read]" ||
		toolOnly.Provenance.Model != "synthetic-model-b" || len(toolOnly.Tools) != 1 ||
		!toolOnly.Tools[0].HadError || toolOnly.Tools[0].ErrorMessage != "invented missing file" {
		t.Errorf("tool-only exchange = %+v", toolOnly)
	}

	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{
		"ZCODE-STEP-SENTINEL", "ZCODE-FINISH-SENTINEL", "ZCODE-HIDDEN-SENTINEL",
		"ZCODE-TIMELINE-SENTINEL", "ZCODE-MISSING-USER-MODEL-SENTINEL",
		"ZCODE-MISSING-ASSISTANT-MODEL-SENTINEL",
	} {
		if strings.Contains(string(encoded), sentinel) {
			t.Errorf("excluded ZCode record entered normalized content: %s", sentinel)
		}
	}
}

func TestZCodeTelemetryDiscardsBelongOnlyToProjectedSessions(t *testing.T) {
	path := syntheticZCodeContent(t)
	db := openSynthetic(t, path)
	exec(t, db, `INSERT INTO session VALUES (
		'synthetic-malformed-session','synthetic-project','synthetic-workspace',NULL,
		'synthetic-malformed-session','/synthetic/workspace/opal',NULL,
		'Malformed synthetic session','0.16.5','interactive',1785542500000,1785542510000)`)
	exec(t, db, `INSERT INTO message VALUES (
		'malformed-message','synthetic-malformed-session',1785542500000,1785542500000,'{',0)`)
	exec(t, db, `INSERT INTO part VALUES (
		'malformed-session-step','malformed-message','synthetic-malformed-session',
		1785542500000,1785542500000,
		'{"type":"step-start","snapshot":"MALFORMED-SESSION-TELEMETRY"}',0)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	records, complaints, err := ReadZCode(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 1 || !strings.Contains(complaints[0], "malformed_json") {
		t.Fatalf("complaints = %v", complaints)
	}
	byReason := map[string]int{}
	for _, discard := range records.Discards {
		if discard.ByDesign {
			byReason[discard.Reason]++
		}
	}
	if byReason["ZCode step telemetry"] != 2 || byReason["ZCode timeline message"] != 1 ||
		byReason["ZCode timeline telemetry"] != 0 {
		t.Fatalf("telemetry discards = %v", byReason)
	}
}

func TestZCodeIngestWritesCorpusProvenanceAndFileStateIncrementally(t *testing.T) {
	path := syntheticZCodeContent(t)
	db := rocaDatabase(t)
	options := Options{Roots: Roots{ZCodeDB: path}}
	first, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.DetectedAgents) != 1 || first.DetectedAgents[0] != "zcode" {
		t.Fatalf("detected agents = %v", first.DetectedAgents)
	}
	if got := first.MessageCoverage["zcode"]; got.Seen != 8 || got.Converted != 3 {
		t.Fatalf("ZCode message coverage = %+v", got)
	}

	checks := []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM sessions WHERE session_id = 'zcode:synthetic-zcode-session'
		  AND source_agent = 'zcode' AND source_surface = 'ZCode'`, 1},
		{`SELECT COUNT(*) FROM exchanges WHERE session_id = 'zcode:synthetic-zcode-session'`, 3},
		{`SELECT COUNT(*) FROM exchanges WHERE session_id = 'zcode:synthetic-zcode-session'
		  AND COALESCE(model, '') = ''`, 0},
		{`SELECT COUNT(*) FROM thinking_blocks WHERE session_id = 'zcode:synthetic-zcode-session'`, 1},
		{`SELECT COUNT(*) FROM tool_uses WHERE session_id = 'zcode:synthetic-zcode-session'`, 2},
		{`SELECT COUNT(*) FROM ingest_file_state WHERE path = ? AND source_kind = 'zcode_database'
		  AND source_agent = 'zcode'`, 1},
	}
	for _, check := range checks {
		var got int
		if err := db.SQL().QueryRow(check.query, path).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Errorf("query returned %d, want %d: %s", got, check.want, check.query)
		}
	}
	var fingerprint string
	if err := db.SQL().QueryRow(`SELECT fingerprint FROM ingest_file_state WHERE path = ?`, path).
		Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fingerprint, ":parser:zcode-3.10.2-v1") {
		t.Fatalf("ZCode state fingerprint = %q", fingerprint)
	}

	second, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesRead != 0 || second.FilesSkipped != 1 || second.Delta != (Tables{}) {
		t.Fatalf("idempotent run = files read/skipped %d/%d delta %+v",
			second.FilesRead, second.FilesSkipped, second.Delta)
	}
	if got := second.MessageCoverage["zcode"]; got.Seen != 8 || got.Converted != 3 {
		t.Fatalf("idempotent ZCode coverage = %+v", got)
	}
}

func TestZCodeSchemaChangeIsRefusedByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	db := openSynthetic(t, path)
	exec(t, db, `CREATE TABLE schema_migration (id TEXT, checksum TEXT, app_version TEXT, time_applied INTEGER)`)
	exec(t, db, `CREATE TABLE session (id TEXT, project_id TEXT, workspace_id TEXT, parent_id TEXT,
		slug TEXT, directory TEXT, path TEXT, title TEXT, version TEXT, task_type TEXT,
		time_created INTEGER, time_updated INTEGER)`)
	exec(t, db, `CREATE TABLE message (id TEXT, session_id TEXT, time_created INTEGER,
		time_updated INTEGER, data TEXT, sequence INTEGER)`)
	exec(t, db, `CREATE TABLE part (id TEXT, message_id TEXT, session_id TEXT,
		time_created INTEGER, time_updated INTEGER, sequence INTEGER)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadZCode(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "ZCode") || !strings.Contains(err.Error(), "data") {
		t.Fatalf("schema refusal = %v", err)
	}
}

func TestZCodeFingerprintIncludesItsAppPinAndWAL(t *testing.T) {
	path := syntheticZCodeContent(t)
	target := Target{Path: path, Kind: parsers.KindZCodeDB}
	fingerprint, err := targetFingerprint(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fingerprint, ":parser:zcode-3.10.2-v1") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if !incrementalityTarget(target).IncludeSQLiteWAL {
		t.Fatal("ZCode fingerprint excludes live WAL content")
	}
}

func TestZCodeRealHarvest(t *testing.T) {
	if os.Getenv("ROCA_REAL_HARVEST") != "1" {
		t.Skip("set ROCA_REAL_HARVEST=1 to read the present ZCode store")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if !isFile(path) {
		t.Skip("ZCode store is not present on this machine")
	}
	records, complaints, err := ReadZCode(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var exchanges, thinking, tools int
	for _, session := range records.Sessions {
		exchanges += len(session.Exchanges)
		for _, exchange := range session.Exchanges {
			if exchange.Provenance.Model == "" {
				t.Fatalf("real ZCode exchange %q has no model", exchange.SourceID)
			}
			thinking += len(exchange.Thinking)
			tools += len(exchange.Tools)
		}
	}
	if len(records.Sessions) == 0 || exchanges == 0 {
		t.Fatalf("near-zero ZCode conversion: sessions=%d exchanges=%d coverage=%+v complaints=%d",
			len(records.Sessions), exchanges, records.MessageCoverage, len(complaints))
	}
	t.Logf("ZCode real harvest: sessions=%d exchanges=%d thinking=%d tools=%d deferred=%d complaints=%d coverage=%+v",
		len(records.Sessions), exchanges, thinking, tools, records.Deferred, len(complaints), records.MessageCoverage)

	dbPath := os.Getenv("ROCA_ZCODE_TEST_DB")
	if dbPath == "" {
		return
	}
	privateRoot := filepath.Join(home, ".roca") + string(filepath.Separator)
	if strings.HasPrefix(filepath.Clean(dbPath)+string(filepath.Separator), privateRoot) {
		t.Fatalf("refusing real-harvest test database under %s", privateRoot)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), db, registry(t), Options{Roots: Roots{ZCodeDB: path}})
	if err != nil {
		t.Fatal(err)
	}
	var landed, missingModel, state int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_agent = 'zcode'`).Scan(&landed); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges e JOIN sessions s USING (session_id)
		WHERE s.source_agent = 'zcode' AND COALESCE(e.model, '') = ''`).Scan(&missingModel); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM ingest_file_state
		WHERE source_agent = 'zcode' AND source_kind = 'zcode_database'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if landed == 0 || missingModel != 0 || state != 1 {
		t.Fatalf("real ZCode corpus: sessions=%d missing_model=%d state=%d result=%+v",
			landed, missingModel, state, result.Delta)
	}
	t.Logf("ZCode real corpus: sessions=%d exchanges=%d missing_model=%d state=%d db=%s",
		landed, exchanges, missingModel, state, dbPath)
}
