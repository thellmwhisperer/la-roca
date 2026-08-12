package sqlrepair_test

import (
	"reflect"
	"strings"
	"testing"

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
			name: "soft union repair",
			raw: "SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 5 " +
				"UNION ALL SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 7",
			wantSQL: "SELECT id, created_at AS occurred_at FROM memories UNION ALL " +
				"SELECT id, created_at AS occurred_at FROM memories ORDER BY occurred_at DESC LIMIT 7",
			wantRepairs: []string{sqlrepair.UnionOrderBy},
		},
		{
			name: "aggressive union fallback",
			raw: "SELECT id FROM memories ORDER BY id LIMIT 5 UNION ALL " +
				"SELECT id FROM exchanges ORDER BY id LIMIT 5 ORDER BY id LIMIT 9",
			wantSQL:     "SELECT id FROM memories UNION ALL SELECT id FROM exchanges ORDER BY id LIMIT 9",
			wantRepairs: []string{sqlrepair.UnionOrderBy},
		},
		{
			name:        "FTS phrase followed by parenthesized OR group",
			raw:         `SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"Javi" ("objetivo" OR "propósito" OR "carrera" OR "impulsa" OR "motivación")'`,
			wantSQL:     `SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"Javi" AND ("objetivo" OR "propósito" OR "carrera" OR "impulsa" OR "motivación")'`,
			wantRepairs: []string{sqlrepair.FTSOrGroup},
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
		"SELECT id FROM memories ORDER BY id UNION SELECT id FROM exchanges " +
			"UNION ALL SELECT id FROM sessions ORDER BY id",
		`SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"alpha" AND ("beta" OR "gamma")'`,
		`SELECT rowid FROM memories_fts WHERE memories_fts MATCH '"alpha" NOT ("beta" OR "gamma")'`,
	}
	for _, raw := range benchCases {
		got := sqlrepair.Prepare(raw)
		if got.SQL != strings.TrimSpace(raw) || len(got.Repairs) != 0 {
			t.Errorf("Prepare(%q) = SQL %q repairs %v", raw, got.SQL, got.Repairs)
		}
	}
}
