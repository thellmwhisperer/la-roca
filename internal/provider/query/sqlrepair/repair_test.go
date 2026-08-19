package sqlrepair_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rqlite "github.com/rqlite/sql"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlrepair"
)

func TestPrepareRepairsOnlyNamedModelOutputShapes(t *testing.T) {
	benchCases := []struct {
		name, raw, wantSQL string
		wantRepairs        []string
	}{
		{
			name: "SQL fence", raw: "```sql\nSELECT content FROM memories LIMIT 5\n```",
			wantSQL: "SELECT content FROM memories LIMIT 5", wantRepairs: []string{sqlrepair.CodeFence},
		},
		{
			name: "bare fence with prose", raw: "Here is the query:\n```\nSELECT content FROM memories LIMIT 5;\n```\nThis is the result.",
			wantSQL: "SELECT content FROM memories LIMIT 5", wantRepairs: []string{
				sqlrepair.CodeFence, sqlrepair.SurroundingProse, sqlrepair.TrailingSemicolon,
			},
		},
		{
			name: "unfenced prose", raw: "The query is:\nSELECT content FROM memories LIMIT 5;\nHope this helps.",
			wantSQL: "SELECT content FROM memories LIMIT 5", wantRepairs: []string{
				sqlrepair.SurroundingProse, sqlrepair.TrailingSemicolon,
			},
		},
		{
			name: "reasoning and repetition loop",
			raw: "<think>draft it</think>\n" +
				"SELECT content FROM memories WHERE supersedes IS NULL LIMIT 5\n" +
				"SELECT content FROM memories WHERE supersedes IS NULL LIMIT 5",
			wantSQL:     "SELECT content FROM memories WHERE supersedes IS NULL LIMIT 5",
			wantRepairs: []string{sqlrepair.ThinkingBlock, sqlrepair.RepetitionLoop},
		},
		{
			name: "soft union repair wraps the leading branch",
			raw: "SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 5 " +
				"UNION ALL SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 7",
			wantSQL: "SELECT * FROM (SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 5) " +
				"UNION ALL SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 7",
			wantRepairs: []string{sqlrepair.WrapOrderedCompound},
		},
		{
			name: "repeated trailing ORDER BY keeps the last clause after wrap",
			raw: "SELECT id FROM memories ORDER BY id LIMIT 5 UNION ALL " +
				"SELECT id FROM exchanges ORDER BY id LIMIT 5 ORDER BY id LIMIT 9",
			wantSQL:     "SELECT * FROM (SELECT id FROM memories ORDER BY id LIMIT 5) UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 9",
			wantRepairs: []string{sqlrepair.UnionOrderBy, sqlrepair.WrapOrderedCompound},
		},
		{
			name:        "FTS phrase followed by parenthesized OR group",
			raw:         `SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"Javi" ("objetivo" OR "propósito" OR "carrera" OR "impulsa" OR "motivación")'`,
			wantSQL:     `SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"Javi" AND ("objetivo" OR "propósito" OR "carrera" OR "impulsa" OR "motivación")'`,
			wantRepairs: []string{sqlrepair.FTSOrGroup},
		},
		{
			name: "qualify a unique JOIN ORDER BY column",
			raw: "SELECT s.session_id, DATE(s.started_at) AS session_date, COUNT(*) AS tool_count " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id, DATE(s.started_at) ORDER BY session_date DESC, session_id LIMIT 1000",
			wantSQL: "SELECT s.session_id, DATE(s.started_at) AS session_date, COUNT(*) AS tool_count " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id, DATE(s.started_at) ORDER BY session_date DESC, s.session_id LIMIT 1000",
			wantRepairs: []string{sqlrepair.JoinOrderBy},
		},
		{
			name: "qualify several unique JOIN ORDER BY columns",
			raw: "SELECT 'é' AS label, s.source_agent, tu.tool_name " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"ORDER BY source_agent, tool_name LIMIT 10",
			wantSQL: "SELECT 'é' AS label, s.source_agent, tu.tool_name " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"ORDER BY s.source_agent, tu.tool_name LIMIT 10",
			wantRepairs: []string{sqlrepair.JoinOrderBy},
		},
		{
			name: "leave an explicit ORDER BY alias bare",
			raw: "SELECT s.session_id AS session_key, COUNT(*) AS cnt " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id ORDER BY session_key LIMIT 10",
			wantSQL: "SELECT s.session_id AS session_key, COUNT(*) AS cnt " +
				"FROM tool_uses tu JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id ORDER BY session_key LIMIT 10",
		},
		{
			name: "leave an ambiguous JOIN ORDER BY column for the gate",
			raw: "SELECT s.session_id, tu.session_id FROM tool_uses tu " +
				"JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id, tu.session_id ORDER BY session_id LIMIT 10",
			wantSQL: "SELECT s.session_id, tu.session_id FROM tool_uses tu " +
				"JOIN sessions s ON tu.session_id = s.session_id " +
				"GROUP BY s.session_id, tu.session_id ORDER BY session_id LIMIT 10",
		},
		{
			name: "wrap a leading UNION branch that asked for its own LIMIT",
			raw:  "SELECT id FROM memories ORDER BY id LIMIT 5 UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 7",
			wantSQL: "SELECT * FROM (SELECT id FROM memories ORDER BY id LIMIT 5) " +
				"UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 7",
			wantRepairs: []string{sqlrepair.WrapOrderedCompound},
		},
		{
			name:    "leave a trailing compound ORDER BY on a bare last branch",
			raw:     "SELECT id FROM memories UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 7",
			wantSQL: "SELECT id FROM memories UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 7",
		},
		{
			name: "keep a compound ORDER BY projected by a later branch",
			raw: "SELECT id, created_at FROM memories UNION ALL " +
				"SELECT id, agent_timestamp FROM exchanges ORDER BY agent_timestamp DESC LIMIT 10",
			wantSQL: "SELECT id, created_at FROM memories UNION ALL " +
				"SELECT id, agent_timestamp FROM exchanges ORDER BY agent_timestamp DESC LIMIT 10",
		},
		{
			name: "keep a compound ORDER BY aliased by a later branch",
			raw: "SELECT id, created_at FROM memories UNION ALL " +
				"SELECT id, agent_timestamp AS occurred_at FROM exchanges ORDER BY occurred_at DESC",
			wantSQL: "SELECT id, created_at FROM memories UNION ALL " +
				"SELECT id, agent_timestamp AS occurred_at FROM exchanges ORDER BY occurred_at DESC",
		},
		{
			name: "drop a compound ORDER BY that no branch projects",
			raw: "SELECT id, created_at FROM memories UNION ALL " +
				"SELECT id, agent_timestamp FROM exchanges ORDER BY missing_timestamp DESC LIMIT 10",
			wantSQL:     "SELECT id, created_at FROM memories UNION ALL SELECT id, agent_timestamp FROM exchanges LIMIT 10",
			wantRepairs: []string{sqlrepair.WrapOrderedCompound},
		},
		{
			name: "keep a compound ORDER BY projected expression",
			raw: "SELECT lower(layer) FROM memories UNION ALL " +
				"SELECT lower(project) FROM sessions ORDER BY lower(layer)",
			wantSQL: "SELECT lower(layer) FROM memories UNION ALL " +
				"SELECT lower(project) FROM sessions ORDER BY lower(layer)",
		},
		{
			name: "match compound ORDER BY expression identifiers case insensitively",
			raw: "SELECT lower(layer) FROM memories UNION ALL " +
				"SELECT lower(project) FROM sessions ORDER BY LOWER(LAYER)",
			wantSQL: "SELECT lower(layer) FROM memories UNION ALL " +
				"SELECT lower(project) FROM sessions ORDER BY LOWER(LAYER)",
		},
		{
			name: "preserve literal case when matching compound expressions",
			raw: "SELECT CASE WHEN layer = 'A' THEN 1 END FROM memories UNION ALL " +
				"SELECT 1 FROM sessions ORDER BY CASE WHEN layer = 'a' THEN 1 END",
			wantSQL:     "SELECT CASE WHEN layer = 'A' THEN 1 END FROM memories UNION ALL SELECT 1 FROM sessions",
			wantRepairs: []string{sqlrepair.WrapOrderedCompound},
		},
		{
			name: "fold identifiers inside compound scalar subqueries",
			raw: "SELECT (SELECT layer) FROM memories UNION ALL " +
				"SELECT (SELECT project) FROM sessions ORDER BY (SELECT LAYER)",
			wantSQL: "SELECT (SELECT layer) FROM memories UNION ALL " +
				"SELECT (SELECT project) FROM sessions ORDER BY (SELECT LAYER)",
		},
		{
			name: "keep compound ORDER BY aliases and valid ordinals",
			raw: "SELECT lower(layer) AS normalized, project FROM memories UNION ALL " +
				"SELECT lower(project), agent FROM sessions ORDER BY normalized, 2",
			wantSQL: "SELECT lower(layer) AS normalized, project FROM memories UNION ALL " +
				"SELECT lower(project), agent FROM sessions ORDER BY normalized, 2",
		},
		{
			name:        "rewrite the JSON arrow shorthand to json_extract",
			raw:         "SELECT id FROM memories WHERE metadata -> '$.project' = 'galactic' LIMIT 5",
			wantSQL:     "SELECT id FROM memories WHERE json_extract(metadata, '$.project') = 'galactic' LIMIT 5",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:        "rewrite a complete JSON arrow chain",
			raw:         "SELECT metadata -> '$.project' ->> '$.name' FROM memories",
			wantSQL:     "SELECT json_extract(json_extract(metadata, '$.project'), '$.name') FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:        "rewrite a grouped JSON operand",
			raw:         "SELECT (metadata) -> '$.project' FROM memories",
			wantSQL:     "SELECT json_extract((metadata), '$.project') FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:        "rewrite a parenthesized JSON chain atomically",
			raw:         "SELECT (metadata -> '$.project') ->> '$.name' FROM memories",
			wantSQL:     "SELECT json_extract((json_extract(metadata, '$.project')), '$.name') FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name: "rewrite nested JSON arrows in a multi-argument call",
			raw: "SELECT COALESCE(metadata -> '$.project', '{}') -> '$.name' = 'Javi' " +
				"FROM memories",
			wantSQL: "SELECT json_extract(COALESCE(json_extract(metadata, '$.project'), '{}'), '$.name') = 'Javi' " +
				"FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name: "rewrite a JSON arrow after a CASE expression",
			raw: "SELECT CASE WHEN metadata IS NULL THEN '{}' ELSE metadata END -> '$.project' " +
				"FROM memories",
			wantSQL: "SELECT json_extract(CASE WHEN metadata IS NULL THEN '{}' ELSE metadata END, '$.project') " +
				"FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:        "rewrite a JSON arrow after multibyte text",
			raw:         "SELECT 'é', metadata -> '$.project' FROM memories",
			wantSQL:     "SELECT 'é', json_extract(metadata, '$.project') FROM memories",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:        "rewrite a JSON arrow inside a scalar subquery",
			raw:         "SELECT (SELECT metadata -> '$.project' FROM memories LIMIT 1)",
			wantSQL:     "SELECT (SELECT json_extract(metadata, '$.project') FROM memories LIMIT 1)",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:    "leave a JSON label operand unchanged",
			raw:     "SELECT metadata ->> 'project' FROM memories",
			wantSQL: "SELECT metadata ->> 'project' FROM memories",
		},
		{
			name:    "leave a JSON index operand unchanged",
			raw:     "SELECT metadata -> 0 FROM memories",
			wantSQL: "SELECT metadata -> 0 FROM memories",
		},
		{
			name:        "rewrite the JSON SQL-value arrow to json_extract",
			raw:         "SELECT id FROM memories WHERE metadata ->> '$.project' = 'galactic' LIMIT 5",
			wantSQL:     "SELECT id FROM memories WHERE json_extract(metadata, '$.project') = 'galactic' LIMIT 5",
			wantRepairs: []string{sqlrepair.PreserveJSONExtract},
		},
		{
			name:    "leave an existing json_extract call alone",
			raw:     "SELECT id FROM memories WHERE json_extract(metadata, '$.project') = 'galactic' LIMIT 5",
			wantSQL: "SELECT id FROM memories WHERE json_extract(metadata, '$.project') = 'galactic' LIMIT 5",
		},
	}
	for _, benchCase := range benchCases {
		t.Run(benchCase.name, func(t *testing.T) {
			got := sqlrepair.Prepare(benchCase.raw)
			if got.SQL != benchCase.wantSQL {
				t.Errorf("SQL = %q, want %q", got.SQL, benchCase.wantSQL)
			}
			if !reflect.DeepEqual(got.Repairs, benchCase.wantRepairs) {
				t.Errorf("repairs = %v, want %v", got.Repairs, benchCase.wantRepairs)
			}
		})
	}
}

func TestPrepareDoesNotInventTheMissingHalfOfATruncatedUnion(t *testing.T) {
	// This is the synthetic shape of the live failure: a valid CTE starts two
	// UNION ALL branches and the model runs out immediately after FROM (.
	raw := "WITH results AS (\n" +
		"  SELECT id, content AS text FROM memories\n" +
		"  UNION ALL\n" +
		"  SELECT id, agent_text FROM exchanges\n" +
		"  UNION ALL\n" +
		"  SELECT id, human_text FROM ("
	got := sqlrepair.Prepare(raw)
	if got.SQL != raw || len(got.Repairs) != 0 {
		t.Fatalf("truncated SQL was rewritten: SQL=%q repairs=%v", got.SQL, got.Repairs)
	}
}

func TestPrepareLeavesValidSQLAndSuspiciousMultipleBlocksAlone(t *testing.T) {
	benchCases := []string{
		"SELECT id FROM memories ORDER BY id LIMIT 5",
		"SELECT * FROM (SELECT id FROM memories ORDER BY id LIMIT 5) AS recent " +
			"UNION ALL SELECT * FROM (SELECT id FROM memories ORDER BY id LIMIT 5) AS older",
		"```sql\nSELECT id FROM memories\n```\n```sql\nDELETE FROM memories\n```",
		"Preface on the same line: SELECT id FROM memories LIMIT 5",
		"SELECT id FROM memories UNION SELECT id FROM exchanges " +
			"UNION ALL SELECT id FROM sessions ORDER BY id",
		`SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"alpha" AND ("beta" OR "gamma")'`,
		`SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"alpha" NOT ("beta" OR "gamma")'`,
		"SELECT " + strings.Repeat("COALESCE(content, title, path, label, text), ", 80) + "id FROM memories LIMIT 10",
	}
	for _, raw := range benchCases {
		got := sqlrepair.Prepare(raw)
		if got.SQL != strings.TrimSpace(raw) || len(got.Repairs) != 0 {
			t.Errorf("Prepare(%q) = SQL %q repairs %v", raw, got.SQL, got.Repairs)
		}
	}
}

// Live EOF failures stored complete, parenthesis-balanced UNION SQL. Prepare
// used to crop a repeated COALESCE line that belongs to a later branch, and
// the gate parser then reported EOF at that cut. The five shapes are the
// anonymized live queries.
func TestPrepareKeepsRepeatedUnionCoalesceBranches(t *testing.T) {
	entries, err := os.ReadDir("testdata/union_coalesce")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("fixture count = %d, want 5 live shapes", len(entries))
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata/union_coalesce", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			stmt := strings.TrimSpace(string(raw))
			got := sqlrepair.Prepare(stmt)
			if got.SQL != stmt || len(got.Repairs) != 0 {
				t.Fatalf("Prepare cropped a complete UNION: repairs=%v in=%d out=%d",
					got.Repairs, len(stmt), len(got.SQL))
			}
			if _, err := rqlite.NewParser(strings.NewReader(got.SQL)).ParseStatements(); err != nil {
				t.Fatalf("gate parser rejected the intact UNION: %v", err)
			}
		})
	}
}
