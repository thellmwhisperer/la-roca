package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func syntheticOpenCodeContent(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := openSynthetic(t, path)
	fixture, err := os.ReadFile(filepath.Join("testdata", "opencode-content.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenCodeConvertsEachFinishedMessageAndItsContent(t *testing.T) {
	path := syntheticOpenCodeContent(t)
	records, complaints, err := ReadOpenCode(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 0 || len(records.Sessions) != 1 {
		t.Fatalf("complaints/sessions = %v/%d", complaints, len(records.Sessions))
	}
	coverage := records.MessageCoverage
	if coverage == nil || coverage.Seen != 7 || coverage.Converted != 5 ||
		coverage.Skipped["assistant message still being written"] != 1 ||
		coverage.Skipped["unsupported message role: system"] != 1 {
		t.Fatalf("message coverage = %+v", coverage)
	}

	session := records.Sessions[0]
	if len(session.Exchanges) != 5 {
		t.Fatalf("exchanges = %d, want one per converted message", len(session.Exchanges))
	}
	user, first, second, third, fourth := session.Exchanges[0], session.Exchanges[1],
		session.Exchanges[2], session.Exchanges[3], session.Exchanges[4]
	if user.SourceID != "user-1" || user.HumanText != "map the synthetic beacon" || user.AgentText != "" {
		t.Errorf("user exchange = %+v", user)
	}
	if first.SourceID != "assistant-1" || first.HumanText != "" ||
		!strings.Contains(first.AgentText, "the beacon is mapped") ||
		!strings.Contains(first.AgentText, "synthetic/beacon.txt") ||
		!strings.Contains(first.AgentText, "synthetic-patch-hash") ||
		first.Provenance.Model != "synthetic-model-a" {
		t.Errorf("first assistant exchange = %+v", first)
	}
	if len(first.Thinking) != 1 || first.Thinking[0].Text != "follow the invented coordinates" {
		t.Errorf("thinking = %+v", first.Thinking)
	}
	if len(first.Tools) != 1 || first.Tools[0].Name != "grep" ||
		first.Tools[0].ParamsSummary != `{"pattern":"synthetic beacon"}` {
		t.Errorf("tools = %+v", first.Tools)
	}
	if second.SourceID != "assistant-2" || second.Provenance.Model != "synthetic-model-b" ||
		second.AgentText != "a second independent answer" {
		t.Errorf("second assistant exchange = %+v", second)
	}
	if len(second.Tools) != 1 || !second.Tools[0].HadError ||
		second.Tools[0].ErrorMessage != "invented missing file" {
		t.Errorf("failed tool = %+v", second.Tools)
	}
	if third.SourceID != "assistant-3" || third.AgentText != "" ||
		third.Provenance.Model != "synthetic-model-c" {
		t.Errorf("contentless assistant exchange = %+v", third)
	}
	if fourth.SourceID != "assistant-4" || fourth.HumanText != "" ||
		fourth.AgentText != "[tool write]" || fourth.Provenance.Model != "synthetic-model-d" {
		t.Errorf("tool-only assistant exchange = %+v", fourth)
	}
	if len(fourth.Tools) != 1 || fourth.Tools[0].Name != "write" {
		t.Errorf("tool-only tools = %+v", fourth.Tools)
	}

	encoded, err := json.Marshal(session.Metadata["todos"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "verify the invented map") ||
		!strings.Contains(string(encoded), "archive the synthetic beacon") {
		t.Errorf("todos = %s", encoded)
	}
	todos := session.Metadata["todos"].([]map[string]any)
	if position, exists := todos[0]["position"]; !exists || position != 0 {
		t.Errorf("first todo position = %v, exists %t", position, exists)
	}
	_, structuredFailure := (openCodePart{State: json.RawMessage(
		`{"status":"error","error":{"message":"invented structured failure"}}`,
	)}).toolState()
	if structuredFailure != `{"message":"invented structured failure"}` {
		t.Errorf("structured tool failure = %q", structuredFailure)
	}
	unique, duplicateComplaints := uniqueOpenCodeSessions([]row{
		{"id": "synthetic-duplicate"}, {"id": "synthetic-duplicate"},
	})
	if len(unique) != 1 || len(duplicateComplaints) != 1 {
		t.Errorf("duplicate sessions = %d/%v", len(unique), duplicateComplaints)
	}
	all, _ := json.Marshal(records)
	if strings.Contains(string(all), "TELEMETRY-MUST-NOT-LAND") ||
		strings.Contains(string(all), "EVENT-MUST-NOT-LAND") {
		t.Errorf("telemetry entered normalized records: %s", all)
	}
	if len(records.Discards) != 2 || !records.Discards[0].ByDesign || !records.Discards[1].ByDesign {
		t.Errorf("step telemetry exclusions = %+v", records.Discards)
	}

	db := rocaDatabase(t)
	options := Options{Roots: Roots{OpenCodeDB: path}}
	firstRun, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	for label, result := range map[string]Result{"first": firstRun, "idempotent": secondRun} {
		got := result.MessageCoverage["opencode"]
		if got.Seen != 7 || got.Converted != 5 || len(got.Skipped) != 2 {
			t.Errorf("%s run coverage = %+v", label, got)
		}
	}
	if secondRun.FilesRead != 0 || secondRun.FilesSkipped != 1 || secondRun.Delta != (Tables{}) {
		t.Errorf("idempotent run = files read/skipped %d/%d delta %+v",
			secondRun.FilesRead, secondRun.FilesSkipped, secondRun.Delta)
	}
}

func TestOpenCodeBackfillSplitsLegacyPairsWithoutDuplicates(t *testing.T) {
	records, _, err := ReadOpenCode(context.Background(), syntheticOpenCodeContent(t))
	if err != nil {
		t.Fatal(err)
	}
	// Put an unmapped sibling before the mapped assistant and give both the
	// same timestamp. Source order must not let the new identity steal the
	// mapped row's anchor before its owner is visited.
	records.Sessions[0].Exchanges[2].AgentTimestamp =
		records.Sessions[0].Exchanges[1].AgentTimestamp
	records.Sessions[0].Exchanges[0], records.Sessions[0].Exchanges[2] =
		records.Sessions[0].Exchanges[2], records.Sessions[0].Exchanges[0]
	db := rocaDatabase(t)
	ctx := context.Background()
	fingerprints := map[string]string{}
	assistantOneText := ""
	for _, exchange := range records.Sessions[0].Exchanges {
		fingerprints[exchange.SourceID] = exchange.Fingerprint
		if exchange.SourceID == "assistant-1" {
			assistantOneText = exchange.AgentText
		}
	}
	legacyDocument := map[string]any{"opencode": map[string]any{
		"source_exchange_ids": map[string]any{
			"user-1": 1, "assistant-1": 2, "assistant-3": 2, "deleted-message": 5,
		},
		"source_exchange_fingerprints": map[string]any{
			"user-1": "legacy-pair", "assistant-1": fingerprints["assistant-1"],
			"assistant-3": fingerprints["assistant-3"], "deleted-message": "deleted",
		},
	}}
	legacyMetadata, err := json.Marshal(legacyDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sessions
			(session_id, source_agent, metadata) VALUES (?, 'opencode', ?)`,
			"opencode:synthetic-opencode-session", string(legacyMetadata)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exchanges
			(session_id, exchange_number, human_text, agent_text, human_timestamp,
			 agent_timestamp, model) VALUES (?, 1, ?, ?, ?, ?, ?)`,
			"opencode:synthetic-opencode-session", "map the synthetic beacon",
			"the beacon is mapped", "2026-08-01T00:00:00Z", "2026-08-01T00:00:02Z",
			"synthetic-model-a"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exchanges
			(session_id, exchange_number, agent_text, agent_timestamp, model)
			VALUES (?, 2, ?, ?, 'synthetic-model-a')`,
			"opencode:synthetic-opencode-session", assistantOneText,
			isoFromMS(1785542402000)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO exchanges
			(session_id, exchange_number, human_timestamp) VALUES (?, 5, ?)`,
			"opencode:synthetic-opencode-session", isoFromMS(1785542400000)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO thinking_blocks
			(session_id, exchange_number, full_text) VALUES (?, 1, 'legacy thinking')`,
			"opencode:synthetic-opencode-session"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tool_uses
			(session_id, exchange_number, tool_name) VALUES (?, 1, 'legacy-tool')`,
			"opencode:synthetic-opencode-session"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO thinking_blocks
			(session_id, exchange_number, full_text) VALUES (?, 2, 'follow the invented coordinates')`,
			"opencode:synthetic-opencode-session"); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tool_uses
			(session_id, exchange_number, tool_name) VALUES (?, 2, 'grep')`,
			"opencode:synthetic-opencode-session")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	write := func() Counts {
		t.Helper()
		var counts Counts
		if err := db.Write(ctx, func(tx *sql.Tx) error {
			var err error
			counts, err = WriteRecords(ctx, tx, registry(t), records)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return counts
	}
	first := write()
	if first.Exchanges != 3 || first.ExchangesDeleted != 1 {
		t.Fatalf("backfill counts = %+v, want three recovered and one stale duplicate deleted", first)
	}
	if second := write(); second.Exchanges != 0 {
		t.Fatalf("idempotent backfill = %+v", second)
	}
	for i := range records.Sessions[0].Exchanges {
		exchange := &records.Sessions[0].Exchanges[i]
		if exchange.SourceID == "user-1" {
			exchange.HumanText = "a later edited prompt must not rewrite history"
			exchange.Fingerprint = "later-edit"
		}
	}
	if changed := write(); changed.ExchangesChanged != 1 || changed.Exchanges != 0 {
		t.Fatalf("later source edit = %+v, want one frozen changed row", changed)
	}

	var exchanges, userRows, assistantRows, legacyChildren int
	queries := []struct {
		query string
		out   *int
	}{
		{`SELECT COUNT(*) FROM exchanges WHERE session_id = 'opencode:synthetic-opencode-session'`, &exchanges},
		{`SELECT COUNT(*) FROM exchanges WHERE session_id = 'opencode:synthetic-opencode-session'
		  AND human_text = 'map the synthetic beacon' AND agent_text IS NULL AND model IS NULL`, &userRows},
		{`SELECT COUNT(*) FROM exchanges WHERE session_id = 'opencode:synthetic-opencode-session'
		  AND human_text IS NULL AND model IS NOT NULL`, &assistantRows},
		{`SELECT (SELECT COUNT(*) FROM thinking_blocks WHERE session_id = 'opencode:synthetic-opencode-session'
		  AND full_text = 'legacy thinking') + (SELECT COUNT(*) FROM tool_uses
		  WHERE session_id = 'opencode:synthetic-opencode-session' AND tool_name = 'legacy-tool')`, &legacyChildren},
	}
	for _, query := range queries {
		if err := db.SQL().QueryRow(query.query).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if exchanges != 5 || userRows != 1 || assistantRows != 4 || legacyChildren != 0 {
		t.Fatalf("exchanges/user/assistants/legacy-children = %d/%d/%d/%d",
			exchanges, userRows, assistantRows, legacyChildren)
	}
	rows, err := db.SQL().Query(`SELECT COALESCE(model, '') FROM exchanges
		WHERE session_id = 'opencode:synthetic-opencode-session' ORDER BY exchange_number`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var models []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			t.Fatal(err)
		}
		models = append(models, model)
	}
	if strings.Join(models, ",") != ",synthetic-model-a,synthetic-model-b,synthetic-model-c,synthetic-model-d" {
		t.Errorf("per-message models = %v", models)
	}

	allExchanges := records.Sessions[0].Exchanges
	records.Sessions[0].SnapshotUpdatedAt = "2026-08-01T00:00:00Z"
	records.Sessions[0].Exchanges = allExchanges[:1]
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET metadata = json_set(metadata,
			'$.updated_at', '2026-08-02T00:00:00Z') WHERE session_id = ?`,
			"opencode:synthetic-opencode-session")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stale := write(); stale.ExchangesDeleted != 0 {
		t.Fatalf("stale snapshot pruned current rows: %+v", stale)
	}
	records.Sessions[0].SnapshotUpdatedAt = ""
	records.Sessions[0].Exchanges = nil
	if empty := write(); empty.ExchangesDeleted != 5 {
		t.Fatalf("empty authoritative snapshot = %+v, want five deleted", empty)
	}
	var remaining, mapped int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchanges
		WHERE session_id = 'opencode:synthetic-opencode-session'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sessions, json_each(
		json_extract(metadata, '$.opencode.source_exchange_ids'))
		WHERE session_id = 'opencode:synthetic-opencode-session'`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 || mapped != 0 {
		t.Fatalf("empty snapshot left exchanges/map entries = %d/%d", remaining, mapped)
	}
}

func TestOpenCodeTelegramBotLogExtraction(t *testing.T) {
	for _, test := range []struct {
		name, file, line, session, date string
		count                           int
	}{
		{
			name: "created session with inline timestamp",
			file: "bot-2026-08-17.log",
			line: "[2026-08-18T00:12:34.567Z] [Bot] Created new session via /new command: " +
				"id=ses_synthetic_created, title=Invented",
			session: "ses_synthetic_created", date: "2026-08-18T00:12:34.567Z", count: 1,
		},
		{
			name: "repeated use falls back to file date",
			file: "bot-2026-08-19.log",
			line: "[Bot] Using existing session: id=ses_synthetic_existing " +
				"id=ses_synthetic_existing",
			session: "ses_synthetic_existing", date: "2026-08-19", count: 2,
		},
		{
			name:    "non OpenCode id is ignored",
			file:    "bot-2026-08-20.log",
			line:    "[Bot] Using existing session: id=thread_synthetic",
			session: "thread_synthetic", count: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), test.file)
			if err := os.WriteFile(path, []byte(test.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			evidence, warnings := readOpenCodeTelegramEvidence([]string{path})
			if len(warnings) != 0 {
				t.Fatalf("warnings = %v", warnings)
			}
			entries := evidence[test.session]
			if len(entries) != test.count {
				t.Fatalf("evidence = %+v, want %d entries", entries, test.count)
			}
			for _, raw := range entries {
				entry := raw.(map[string]any)
				if entry["log_file"] != path || entry["line_date"] != test.date {
					t.Errorf("provenance = %+v", entry)
				}
			}
		})
	}
}

func TestOpenCodeTelegramBotEnrichmentIsDurableAndIdempotent(t *testing.T) {
	path := syntheticOpenCodeContent(t)
	source := openSynthetic(t, path)
	for _, table := range []string{"session", "message", "part", "todo"} {
		column := "session_id"
		if table == "session" {
			column = "id"
		}
		exec(t, source, "UPDATE "+table+" SET "+column+" = ? WHERE "+column+" = ?",
			"ses_synthetic_telegram", "synthetic-opencode-session")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	logRoot := filepath.Join(t.TempDir(), "bot-logs")
	if err := os.MkdirAll(logRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logRoot, "bot-2026-08-18.log")
	log := "[2026-08-18T00:30:00Z] [Bot] Created new session via /new command: " +
		"id=ses_synthetic_telegram, title=Synthetic route\n"
	if err := os.WriteFile(logPath, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	db := rocaDatabase(t)
	options := Options{Roots: Roots{
		OpenCodeDB: path, OpenCodeTelegramLogs: logRoot,
	}}
	first, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 {
		t.Fatalf("first run errors = %+v", first.ErrorDetails)
	}
	assertTelegram := func(stage string) {
		t.Helper()
		var channel, provenancePath, provenanceDate string
		err := db.SQL().QueryRow(`
			SELECT json_extract(metadata, '$.channel'),
			       json_extract(value, '$.log_file'),
			       json_extract(value, '$.line_date')
			FROM sessions,
			     json_each(json_extract(metadata,
			       '$.channel_provenance.opencode_telegram_bot.evidence'))
			WHERE session_id = 'opencode:ses_synthetic_telegram'
			LIMIT 1`).Scan(&channel, &provenancePath, &provenanceDate)
		if err != nil {
			t.Fatalf("%s enrichment: %v", stage, err)
		}
		if channel != "telegram" || provenancePath != logPath ||
			provenanceDate != "2026-08-18T00:30:00Z" {
			t.Errorf("%s enrichment = %q/%q/%q", stage,
				channel, provenancePath, provenanceDate)
		}
	}
	assertTelegram("first run")

	second, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesRead != 0 {
		t.Errorf("idempotent run read %d files", second.FilesRead)
	}
	if second.Delta != (Tables{}) {
		t.Errorf("idempotent run delta = %+v", second.Delta)
	}

	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logRoot); err != nil {
		t.Fatal(err)
	}
	rotated, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Errors != 0 {
		t.Fatalf("rotation errors = %+v", rotated.ErrorDetails)
	}
	assertTelegram("after rotation")
	foundExclusion := false
	for _, excluded := range rotated.Coverage.Files.Skips {
		foundExclusion = foundExclusion ||
			excluded.Reason == "OpenCode Telegram bot logs are absent"
	}
	if !foundExclusion {
		t.Errorf("absent directory coverage = %+v", rotated.Coverage.Files.Skips)
	}
}

func TestOpenCodeRealHarvest(t *testing.T) {
	if os.Getenv("ROCA_REAL_HARVEST") != "1" {
		t.Skip("set ROCA_REAL_HARVEST=1 to read the present OpenCode store")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("OpenCode store is not present on this machine")
	} else if err != nil {
		t.Fatal(err)
	}
	records, complaints, err := ReadOpenCode(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	coverage := records.MessageCoverage
	skipped := 0
	for _, count := range coverage.Skipped {
		skipped += count
	}
	thinking, tools, todos := 0, 0, 0
	for _, session := range records.Sessions {
		for _, exchange := range session.Exchanges {
			thinking += len(exchange.Thinking)
			tools += len(exchange.Tools)
		}
		if list, ok := session.Metadata["todos"].([]map[string]any); ok {
			todos += len(list)
		}
	}
	t.Logf("OpenCode real harvest: messages_seen=%d converted=%d skipped=%d sessions=%d thinking=%d tools=%d todos=%d complaints=%d",
		coverage.Seen, coverage.Converted, skipped, len(records.Sessions), thinking, tools, todos,
		len(complaints))
	if coverage.Seen != coverage.Converted+skipped {
		t.Fatalf("message coverage does not balance: %+v", coverage)
	}
	if coverage.Seen >= 1000 && coverage.Converted < coverage.Seen*9/10 {
		t.Fatalf("near-zero OpenCode conversion: %+v", coverage)
	}
	if thinking == 0 || tools == 0 || todos == 0 {
		t.Fatalf("content family missing: thinking=%d tools=%d todos=%d", thinking, tools, todos)
	}
}
