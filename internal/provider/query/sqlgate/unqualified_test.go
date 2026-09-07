package sqlgate_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
)

func TestRejectUnqualifiedNamesTheAttachedCandidates(t *testing.T) {
	schemas := []sqlgate.Schema{
		{Name: "plugin_roca_ops", Tables: []sqlgate.Table{
			{Name: "memories", Columns: []string{"id", "content"}},
			{Name: "layers", Columns: []string{"name"}},
		}},
		{Name: "plugin_roca_corpus", Tables: []sqlgate.Table{
			{Name: "memories", Columns: []string{"id", "content"}},
			{Name: "sessions", Columns: []string{"session_id"}},
		}},
	}
	for _, tc := range []struct {
		name, sql, wantErr string
	}{
		{name: "unqualified memories names both attached copies",
			sql:     `SELECT content FROM memories WHERE content LIKE '%PROCESO CARWOW%'`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "qualified ops memories still runs",
			sql: `SELECT content FROM plugin_roca_ops.memories WHERE content LIKE '%PROCESO CARWOW%'`},
		{name: "qualified corpus memories still runs",
			sql: `SELECT content FROM plugin_roca_corpus.memories LIMIT 5`},
		{name: "expression with no table is unchanged",
			sql: `SELECT 1 AS n`},
		{name: "date expression with no table is unchanged",
			sql: `SELECT CURRENT_DATE, CURRENT_TIME`},
		{name: "one attached copy of layers requires qualification",
			sql:     `SELECT name FROM layers`,
			wantErr: `unqualified table "layers"; candidates: plugin_roca_ops.layers`},
		{name: "one attached copy of sessions requires qualification",
			sql:     `SELECT session_id FROM sessions`,
			wantErr: `unqualified table "sessions"; candidates: plugin_roca_corpus.sessions`},
		{name: "cte named memories is not a table",
			sql: `WITH memories AS (SELECT 1 AS content) SELECT content FROM memories`},
		{name: "cte wrapping an unqualified table still names candidates",
			sql:     `WITH hits AS (SELECT content FROM memories) SELECT content FROM hits`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "main qualifier is unchanged",
			sql: `SELECT content FROM main.memories`},
		{name: "exists subquery names the same candidates",
			sql:     `SELECT 1 WHERE EXISTS (SELECT 1 FROM memories)`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "hidden names are not candidates",
			sql:     `SELECT 1 FROM ingest_file_state`,
			wantErr: `no such table: "ingest_file_state" is not a table this query can read`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := sqlgate.RejectUnqualified(tc.sql, schemas)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("RejectUnqualified(%q) = %v", tc.sql, err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("RejectUnqualified(%q) = %v, want %q", tc.sql, err, tc.wantErr)
			}
		})
	}
}

func TestRejectUnqualifiedUsesTheGateAttachedSchemas(t *testing.T) {
	g := mustOpenGate(t, sqlgate.OpenWithSchemas, []sqlgate.Schema{
		{Name: "plugin_roca_ops", Tables: []sqlgate.Table{{Name: "memories", Columns: []string{"id", "content"}}}},
		{Name: "plugin_roca_corpus", Tables: []sqlgate.Table{{Name: "memories", Columns: []string{"id", "content"}}}},
	})
	_, err := g.Validate(`SELECT content FROM memories LIMIT 5`)
	if err != nil {
		t.Fatalf("Validate still has to prepare core memories: %v", err)
	}
	err = g.RejectUnqualified(`SELECT content FROM memories LIMIT 5`)
	if err == nil || !strings.Contains(err.Error(), `unqualified table "memories"`) ||
		!strings.Contains(err.Error(), "plugin_roca_ops.memories") ||
		!strings.Contains(err.Error(), "plugin_roca_corpus.memories") {
		t.Fatalf("RejectUnqualified = %v", err)
	}
	if _, err := g.Validate(`SELECT content FROM plugin_roca_ops.memories LIMIT 5`); err != nil {
		t.Fatalf("qualified Validate = %v", err)
	}
	if err := g.RejectUnqualified(`SELECT content FROM plugin_roca_ops.memories LIMIT 5`); err != nil {
		t.Fatalf("qualified RejectUnqualified = %v", err)
	}
}

func TestRejectUnqualifiedWithNoAttachedSchemasIsSilent(t *testing.T) {
	if err := sqlgate.RejectUnqualified(`SELECT content FROM memories`, nil); err != nil {
		t.Fatalf("core-only RejectUnqualified = %v", err)
	}
	if _, err := gate(t).Validate(`SELECT content FROM memories LIMIT 5`); err != nil {
		t.Fatalf("core-only Validate = %v", err)
	}
}

func TestRejectUnqualifiedSQLiteExpressionsAndScopes(t *testing.T) {
	schemas := []sqlgate.Schema{
		{Name: "plugin_roca_ops", Tables: []sqlgate.Table{{Name: "memories", Columns: []string{"content", "created_at"}}}},
		{Name: "plugin_roca_corpus", Tables: []sqlgate.Table{{Name: "memories", Columns: []string{"content", "created_at"}}}},
	}
	g := mustOpenGate(t, sqlgate.OpenWithSchemas, schemas)
	for _, query := range []string{
		`SELECT content FROM (WITH hits AS (SELECT content FROM memories) SELECT content FROM hits)`,
		`SELECT content FROM plugin_roca_ops.memories o ORDER BY (SELECT MAX(created_at) FROM memories WHERE content = o.content)`,
		`SELECT COUNT(*) FILTER (WHERE EXISTS (SELECT 1 FROM memories)) FROM plugin_roca_ops.memories`,
		`SELECT row_number() OVER (ORDER BY (SELECT MAX(created_at) FROM memories)) FROM plugin_roca_ops.memories`,
		`SELECT row_number() OVER w FROM plugin_roca_ops.memories WINDOW w AS (PARTITION BY (SELECT content FROM memories))`,
		`WITH hits AS (VALUES ((SELECT content FROM memories))) SELECT * FROM hits`,
		`SELECT 1 WHERE 1 BETWEEN (SELECT COUNT(*) FROM memories) AND 2`,
		`SELECT 1 WHERE 1 BETWEEN 0 AND (SELECT COUNT(*) FROM memories)`,
		`SELECT (SELECT COUNT(*) FROM memories)`,
		`SELECT * FROM memories`,
		`SELECT 1 FROM memories`,
		`SELECT COUNT(*) FROM memories`,
		`SELECT content FROM "memories"`,
		`SELECT content FROM [memories]`,
		`SELECT content FROM 'memories'`,
	} {
		t.Run(query, func(t *testing.T) {
			if _, err := g.Validate(query); err != nil {
				t.Fatalf("SQLite must accept the regression query: %v", err)
			}
			err := g.RejectUnqualified(query)
			if err == nil || !strings.Contains(err.Error(), `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`) {
				t.Fatalf("RejectUnqualified = %v", err)
			}
			qualified := strings.ReplaceAll(query, "FROM memories", "FROM plugin_roca_ops.memories")
			for _, quoted := range []string{`"memories"`, `[memories]`, `'memories'`} {
				qualified = strings.ReplaceAll(qualified, "FROM "+quoted, "FROM plugin_roca_ops.memories")
			}
			if err := g.RejectUnqualified(qualified); err != nil {
				t.Fatalf("qualified query changed: %v", err)
			}
		})
	}
	query := `WITH hits AS (WITH memories AS (SELECT 'x' AS content) SELECT content FROM memories) SELECT m.content FROM hits h JOIN memories m ON m.content = h.content`
	if _, err := g.Validate(query); err != nil {
		t.Fatal(err)
	}
	if err := g.RejectUnqualified(query); err == nil || !strings.Contains(err.Error(), `unqualified table "memories"`) {
		t.Fatalf("nested CTE escaped its scope: %v", err)
	}
	for _, query := range []string{
		`WITH memories AS (SELECT 'x' AS content) SELECT content FROM memories`,
		`WITH memories AS (SELECT 'x' AS content) SELECT COUNT(*) FROM memories`,
		`WITH memories AS (SELECT 'x' AS content) SELECT 1 WHERE EXISTS (SELECT 1 FROM memories)`,
		`WITH memories AS (SELECT 'x' AS content), hits AS (SELECT content FROM memories) SELECT * FROM hits`,
		`WITH RECURSIVE memories(content) AS (SELECT 1 UNION ALL SELECT content+1 FROM memories WHERE content<3) SELECT * FROM memories`,
		`SELECT content FROM (WITH memories AS (SELECT 'x' AS content) SELECT content FROM memories)`,
		`SELECT 'FROM memories' AS content /* FROM memories */`,
	} {
		if err := g.RejectUnqualified(query); err != nil {
			t.Fatalf("CTE or literal query %q changed: %v", query, err)
		}
	}
	if err := g.RejectUnqualified(`SELECT content FROM (`); err == nil {
		t.Fatal("analysis failure must not allow execution")
	}
}
