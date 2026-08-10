package sqlgate_test

import (
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
		"SELECT DATE(started_at) FROM sessions LIMIT 1",
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
	}{
		{"\n  SELECT id FROM memories LIMIT 2000", "LIMIT 1000"},
		{"SELECT id FROM memories LIMIT 2000 OFFSET 4", "LIMIT 1000 OFFSET 4"},
		{"SELECT id FROM memories LIMIT 4, 2000", "LIMIT 4, 1000"},
	} {
		clean, err := gate(t).Validate(tc.statement)
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
	} {
		if _, err := gate(t).Validate(benchCase); err == nil {
			t.Errorf("Validate(%q) passed", benchCase)
		}
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
