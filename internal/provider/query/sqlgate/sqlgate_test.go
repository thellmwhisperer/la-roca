package sqlgate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
)

func gate(t *testing.T) *sqlgate.Gate {
	t.Helper()
	g, err := sqlgate.Open()
	if err != nil {
		t.Fatalf("Open the gate: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestTheGateAcceptsAReadOfTheVisibleTables(t *testing.T) {
	benchCases := []string{
		"SELECT COUNT(*) FROM memories WHERE supersedes IS NULL LIMIT 1",
		"SELECT id, content, created_at FROM memories WHERE layer = 'feedback' LIMIT 5",
		"SELECT AVG(response_latency_ms) FROM exchanges LIMIT 1",
		"SELECT s.project, COUNT(*) FROM sessions s GROUP BY s.project LIMIT 10",
		"SELECT substr(full_text, 1, 200) FROM thinking_blocks LIMIT 3",
		"SELECT json_extract(metadata, '$.for_agent') FROM memories LIMIT 1",
		"SELECT metadata -> '$.for_agent' FROM memories LIMIT 1",
		"SELECT metadata ->> '$.for_agent' FROM memories LIMIT 1",
		"SELECT DATE(started_at) FROM sessions LIMIT 1",
		"SELECT CURRENT_DATE, CURRENT_TIME, CURRENT_TIMESTAMP LIMIT 1",
		"SELECT 'memory' AS source, id FROM memories UNION ALL SELECT 'tool', id FROM tool_uses LIMIT 4",
	}
	for _, benchCase := range benchCases {
		if _, err := gate(t).Validate(benchCase); err != nil {
			t.Errorf("Validate(%q) = %v, want it to pass", benchCase, err)
		}
	}
}

// Only reads are allowed. The message is the acceptance suite's
// contract, so it is checked literally.
func TestTheGateLetsOnlySelectThrough(t *testing.T) {
	benchCases := []string{
		"DELETE FROM memories",
		"UPDATE memories SET content = 'x'",
		"INSERT INTO memories (layer, content, origin) VALUES ('x', 'y', 'agent')",
		"DROP TABLE memories",
		"CREATE TABLE nueva (id INTEGER)",
		"ALTER TABLE memories ADD COLUMN x TEXT",
	}
	for _, benchCase := range benchCases {
		_, err := gate(t).Validate(benchCase)
		if err == nil {
			t.Errorf("Validate(%q) passed", benchCase)
			continue
		}
		if !strings.Contains(err.Error(), "Only SELECT statements are allowed") {
			t.Errorf("Validate(%q) = %v, want the contract's message", benchCase, err)
		}
	}
}

// A chained statement does not slip through. The gate is strict even when a
// small model adds chatter behind a valid first query;
// and whoever has to trim chatter trims it earlier, which is where you know
// whether it came from a model or from an operator.
func TestTheGateRejectsAChainedStatement(t *testing.T) {
	for _, benchCase := range []string{
		"SELECT 1; DROP TABLE memories",
		"SELECT id FROM memories LIMIT 1; DELETE FROM memories",
	} {
		if _, err := gate(t).Validate(benchCase); err == nil {
			t.Errorf("Validate(%q) let two statements through", benchCase)
		}
	}
}

// Table and column existence is the engine's word, not an AST allowlist: the
// statement is prepared against an in-memory database containing only visible
// tables.
func TestTheEngineIsTheOneThatSaysWhatDoesNotExist(t *testing.T) {
	benchCases := []struct{ stmt, dice string }{
		{"SELECT * FROM no_existe LIMIT 1", "no such table"},
		{"SELECT no_existe FROM memories LIMIT 1", "no such column"},
		// The internal state tables are not visible to the query: they do not
		// exist in the database it is prepared against.
		{"SELECT * FROM ingest_file_state LIMIT 1", "no such table"},
		// And neither are v2's, which an adopted database may well carry.
		{"SELECT * FROM proposals LIMIT 1", "no such table"},
		{"SELECT * FROM sqlite_master LIMIT 1", "no such table"},
		{"SELECT file FROM pragma_database_list LIMIT 1", "no such table"},
	}
	for _, benchCase := range benchCases {
		_, err := gate(t).Validate(benchCase.stmt)
		if err == nil {
			t.Errorf("Validate(%q) passed", benchCase.stmt)
			continue
		}
		if !strings.Contains(err.Error(), benchCase.dice) {
			t.Errorf("Validate(%q) = %v, want it to name %q", benchCase.stmt, err, benchCase.dice)
		}
	}
}

func TestAnAttachedSchemaUsesTheSameTableColumnAndFunctionGate(t *testing.T) {
	g, err := sqlgate.OpenWithSchemas([]sqlgate.Schema{{
		Name: "plugin_receipts",
		Tables: []sqlgate.Table{
			{Name: "receipts", Columns: []string{"id", "title"}},
			{Name: "ingest_file_state", Columns: []string{"path"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })

	if _, err := g.Validate(`SELECT title FROM plugin_receipts.receipts LIMIT 5`); err != nil {
		t.Fatalf("valid plugin read was rejected: %v", err)
	}
	for _, statement := range []string{
		`SELECT invented FROM plugin_receipts.receipts LIMIT 5`,
		`SELECT path FROM plugin_receipts.ingest_file_state LIMIT 5`,
		`SELECT readfile(title) FROM plugin_receipts.receipts LIMIT 5`,
	} {
		if _, err := g.Validate(statement); err == nil {
			t.Errorf("plugin statement escaped the gate: %s", statement)
		}
	}
}

func TestHiddenTableClassificationIsSharedAcrossSchemas(t *testing.T) {
	for _, name := range []string{"ingest_file_state", "sqlite_master", "pragma_database_list", "notes_fts_data",
		"plugin_schema", "plugin_migrations", "migration_batches", "custody_memberships", "memory_records",
		"memory_records_fts", "memory_provenance", "memory_compatibility",
		"call_history_segments", "call_history_state", "dedup_runs", "memory_id_remaps",
		"session_id_remaps", "exchange_id_remaps", "thinking_block_id_remaps"} {
		if !sqlgate.IsHiddenTable(name) {
			t.Errorf("%q is not hidden", name)
		}
	}
	if sqlgate.IsHiddenTable("receipts") {
		t.Fatal("ordinary plugin table is hidden")
	}
	if !sqlgate.IsHiddenTable("plugin_roca_corpus.ingest_file_state") {
		t.Fatal("a hidden name stayed visible behind a qualifier")
	}
	if !sqlgate.IsFTSTable("exchanges_fts") || !sqlgate.IsFTSTable("plugin_roca_corpus.memories_fts") {
		t.Fatal("queryable FTS names are not classified as FTS")
	}
	if sqlgate.IsFTSTable("exchanges") || sqlgate.IsFTSTable("memory_records_fts") ||
		sqlgate.IsFTSTable("memories_fts_data") {
		t.Fatal("a base, hidden, or shadow name is classified as FTS")
	}
}

func TestTheGateRejectsFunctionsNotOnTheList(t *testing.T) {
	for _, benchCase := range []string{
		"SELECT load_extension('x') FROM memories LIMIT 1",
		"SELECT readfile('/etc/passwd') FROM memories LIMIT 1",
		"SELECT writefile('x', content) FROM memories LIMIT 1",
	} {
		_, err := gate(t).Validate(benchCase)
		if err == nil {
			t.Errorf("Validate(%q) passed", benchCase)
			continue
		}
		if !strings.Contains(err.Error(), "is not allowed") {
			t.Errorf("Validate(%q) = %v, want it to name the function", benchCase, err)
		}
	}
}

// The LIMIT is a guarantee, not a suggestion: when it does not come, it is
// added; when it comes wildly over, it is clamped.
func TestTheLimitIsAddedAndClamped(t *testing.T) {
	clean, err := gate(t).Validate("SELECT id FROM memories")
	if err != nil {
		t.Fatalf("Validar: %v", err)
	}
	if !strings.Contains(clean, "LIMIT 1000") {
		t.Errorf("without a LIMIT the cap was not injected: %s", clean)
	}

	clean, err = gate(t).Validate("SELECT id FROM memories LIMIT 500000")
	if err != nil {
		t.Fatalf("Validar: %v", err)
	}
	if !strings.Contains(clean, "LIMIT 1000") {
		t.Errorf("a runaway LIMIT was not clamped: %s", clean)
	}

	if _, err := gate(t).Validate("SELECT id FROM memories LIMIT 'muchas'"); err == nil {
		t.Error("a LIMIT that is not a number passed")
	}
}

func TestTheLimitClampHandlesWhitespaceOffsetsAndBothSQLiteForms(t *testing.T) {
	for _, tc := range []struct {
		statement, want string
		wantErr         bool
	}{
		{statement: "\n  SELECT id FROM memories LIMIT 2000", want: "LIMIT 1000"},
		{statement: "SELECT id FROM memories LIMIT 2000 OFFSET 4", want: "LIMIT 1000 OFFSET 4"},
		{statement: "SELECT id FROM memories LIMIT 4, 2000", want: "LIMIT 4, 1000"},
		{statement: "SELECT id FROM memories LIMIT 2000 /* gap */ OFFSET 4", want: "LIMIT 1000 /* gap */ OFFSET 4"},
		{statement: "SELECT id AS [memory;id] FROM memories LIMIT 1", want: "LIMIT 1"},
		{statement: "SELECT id AS [limit;id] FROM memories", want: "LIMIT 1000"},
		{statement: "SELECT id FROM memories LIMIT 1+100000", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT 1e6", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT 1 + 100000", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT 1 OFFSET 1+100000", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT 1, 1e6", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT +1", wantErr: true},
		{statement: "SELECT id FROM memories LIMIT -1", wantErr: true},
	} {
		clean, err := gate(t).Validate(tc.statement)
		if tc.wantErr {
			if err == nil || err.Error() != "LIMIT must be a numeric literal" {
				t.Errorf("Validate(%q) = %v, want numeric-literal contract", tc.statement, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Validate(%q) = %v", tc.statement, err)
			continue
		}
		if !strings.Contains(clean, tc.want) {
			t.Errorf("Validate(%q) = %q, want %q", tc.statement, clean, tc.want)
		}
	}
}

func TestTheLimitIsEffectiveBeforeTrailingComments(t *testing.T) {
	for _, statement := range []string{
		"SELECT id FROM memories -- trailing comment",
		"SELECT id FROM memories /* trailing comment",
		"SELECT id FROM memories LIMIT 500000 -- trailing comment",
	} {
		clean, err := gate(t).Validate(statement)
		if err != nil {
			t.Errorf("Validate(%q) = %v", statement, err)
			continue
		}
		comment := strings.Index(clean, "--")
		if comment < 0 {
			comment = strings.Index(clean, "/*")
		}
		limit := strings.Index(clean, "LIMIT 1000")
		if limit < 0 || comment >= 0 && limit > comment {
			t.Errorf("cap is not effective before the comment: %q", clean)
		}
	}
}

func TestTableValuedPragmasCannotReadSchemaIndirectly(t *testing.T) {
	for _, statement := range []string{
		"SELECT * FROM pragma_table_info('memories')",
		"WITH hidden AS (SELECT * FROM pragma_table_info('ingest_file_state')) SELECT * FROM hidden",
		"SELECT id FROM memories WHERE id IN (SELECT cid FROM pragma_table_info('memories'))",
	} {
		if _, err := gate(t).Validate(statement); err == nil {
			t.Errorf("Validate(%q) exposed schema through a table-valued pragma", statement)
		}
	}
}

func TestTheGateRejectsWhatIsNotEvenAQuery(t *testing.T) {
	for _, benchCase := range []string{
		"",
		"   ",
		"ATTACH DATABASE 'other.db' AS other",
		"PRAGMA table_info(memories)",
		"this is not sql",
		"SELECT id FROM memories\x00",
	} {
		if _, err := gate(t).Validate(benchCase); err == nil {
			t.Errorf("Validate(%q) passed", benchCase)
		}
	}

	_, err := gate(t).Validate("WITH results AS (\n  SELECT id FROM memories\n  UNION ALL\n  SELECT id FROM (")
	if err == nil || !strings.Contains(err.Error(), "SQL parse error") {
		t.Fatalf("incomplete SQL = %v, want a parse error from the engine", err)
	}
	if strings.Contains(err.Error(), "EOF") {
		t.Fatalf("incomplete SQL died as a parser EOF: %v", err)
	}
}

// The five live EOF shapes are complete, parenthesis-balanced UNIONs. The
// engine under the authorization callback is the correctness verdict; a
// grammar-subset parse must not report EOF mid-input. Fixture 01 uses only
// core tables and must pass the whole gate. The others attach plugin names
// the default validation schema does not have, so they may fail as no such
// table, never as a mid-statement EOF.
func TestTheGatePreparesTheLiveUnionShapes(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "sqlrepair", "testdata", "union_coalesce"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("fixture count = %d, want 5 live shapes", len(entries))
	}
	g := gate(t)
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "sqlrepair", "testdata", "union_coalesce", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.Validate(strings.TrimSpace(string(raw)))
			if err != nil && strings.Contains(err.Error(), "EOF") {
				t.Fatalf("complete UNION died as a parse EOF: %v", err)
			}
			if entry.Name() == "01_fts_four_branch.sql" && err != nil {
				t.Fatalf("core-only live shape must pass the gate: %v", err)
			}
		})
	}
}

// A nested SELECT that touches an invisible table does not pass either: the
// engine prepares the whole statement, not its first line.
func TestTheGateAlsoLooksAtSubqueries(t *testing.T) {
	benchCases := []string{
		"SELECT id FROM memories WHERE id IN (SELECT id FROM proposals) LIMIT 1",
		"SELECT (SELECT COUNT(*) FROM ingest_file_state) AS n FROM memories LIMIT 1",
	}
	for _, benchCase := range benchCases {
		if _, err := gate(t).Validate(benchCase); err == nil {
			t.Errorf("Validate(%q) passed with an invisible table inside", benchCase)
		}
	}
}
