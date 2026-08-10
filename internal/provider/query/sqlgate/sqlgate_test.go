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

// Only reads are allowed (F04-06). The message is the consecrated suite's
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

// A chained statement does not slip through (F04-07). The lab keeps the first
// query because a small model adds chatter behind it; here the gate is strict
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
// statement is prepared against an in-memory database that only has the visible
// tables (TECH-SPEC 1.5).
func TestTheEngineIsTheOneThatSaysWhatDoesNotExist(t *testing.T) {
	benchCases := []struct{ stmt, dice string }{
		{"SELECT * FROM no_existe LIMIT 1", "no such table"},
		{"SELECT no_existe FROM memories LIMIT 1", "no such column"},
		// The internal state tables are not visible to the query: they do not
		// exist in the database it is prepared against.
		{"SELECT * FROM ingest_file_state LIMIT 1", "no such table"},
		{"SELECT * FROM queryplan_teach_examples LIMIT 1", "no such table"},
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

func TestTheGateRejectsWhatIsNotEvenAQuery(t *testing.T) {
	for _, benchCase := range []string{
		"",
		"   ",
		"ATTACH DATABASE 'otra.db' AS otra",
		"PRAGMA table_info(memories)",
		"esto no es sql",
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
