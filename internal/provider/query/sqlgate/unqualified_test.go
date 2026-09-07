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
		{name: "one attached copy of layers still resolves",
			sql: `SELECT name FROM layers`},
		{name: "one attached copy of sessions still resolves",
			sql: `SELECT session_id FROM sessions`},
		{name: "cte named memories is not a table",
			sql: `WITH memories AS (SELECT 1 AS content) SELECT content FROM memories`},
		{name: "cte wrapping an unqualified table still names candidates",
			sql:     `WITH hits AS (SELECT content FROM memories) SELECT content FROM hits`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "main qualifier is still unqualified",
			sql:     `SELECT content FROM main.memories`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "exists subquery names the same candidates",
			sql:     `SELECT 1 WHERE EXISTS (SELECT 1 FROM memories)`,
			wantErr: `unqualified table "memories"; candidates: plugin_roca_ops.memories, plugin_roca_corpus.memories`},
		{name: "hidden names are not candidates",
			sql: `SELECT 1 FROM ingest_file_state`},
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
