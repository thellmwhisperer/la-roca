package sqlgate_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
)

// The gate has to know the lexical index: the engine prepares a MATCH, and
// the authorization callback lets bm25 through.
// Without this, the FTS route would be SQL the gate rejects, and the guarantee
// "everything that runs has been validated" would force skipping it.
func TestTheGateAcceptsLexicalIndexSearch(t *testing.T) {
	gate := open(t)
	stmts := []string{
		`SELECT rowid, bm25(memories_fts) FROM memories_fts WHERE memories_fts MATCH '"go"' LIMIT 10`,
		`SELECT m.id, m.content FROM (SELECT rowid AS fila, bm25(memories_fts) AS rango ` +
			`FROM memories_fts WHERE memories_fts MATCH '"go"' ORDER BY rango LIMIT 50) AS f ` +
			`JOIN memories AS m ON m.id = f.fila LIMIT 10`,
		`SELECT rowid FROM exchanges_fts WHERE exchanges_fts MATCH '{human_text} : ("go")' LIMIT 5`,
		`SELECT rowid FROM thinking_fts WHERE thinking_fts MATCH '"go"' LIMIT 5`,
		`SELECT rowid FROM sessions_fts WHERE sessions_fts MATCH '"go"' LIMIT 5`,
	}
	for _, stmt := range stmts {
		if _, err := gate.Validate(stmt); err != nil {
			t.Errorf("the gate rejects a legitimate search:\n%s\n-> %v", stmt, err)
		}
	}
}

// Live ops: the model wrote MATCH/bm25 over plugin FTS tables that exist in
// the attached databases. The gate created those names as ordinary tables, so
// SQLite treated `exchanges_fts` as a column and rejected a read-only census.
// The acceptance is Validate() itself — not how the gate parses — so it holds
// if validation moves to prepare-under-authorizer against a schema-only DB.
func TestTheGateAcceptsFTSMatchOnAttachedPluginTables(t *testing.T) {
	gate, err := sqlgate.OpenWithSchemas([]sqlgate.Schema{
		{
			Name: "plugin_roca_corpus",
			Tables: []sqlgate.Table{
				{Name: "exchanges", Columns: []string{"id", "session_id", "human_text", "agent_text", "human_timestamp"}},
				{Name: "documents_search", Columns: []string{"human_text", "agent_text"}, FTS5: true},
				{Name: "memories", Columns: []string{"id", "content"}},
				{Name: "receipts_fts", Columns: []string{"content"}},
				{Name: "thinking_blocks", Columns: []string{"id", "full_text"}},
			},
		},
		{
			Name: "plugin_records",
			Tables: []sqlgate.Table{
				{Name: "documents_search", Columns: []string{"body"}},
				{Name: "documents_search_data", Columns: []string{"body"}},
				{Name: "records", Columns: []string{"documents_search"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gate.Close() })

	for _, stmt := range []string{
		`SELECT rowid FROM plugin_roca_corpus.documents_search WHERE documents_search MATCH '"la"' LIMIT 5`,
		`SELECT rowid, bm25(documents_search) FROM plugin_roca_corpus.documents_search WHERE documents_search MATCH '"la"' LIMIT 5`,
		`SELECT body FROM plugin_records.documents_search_data LIMIT 5`,
		// Representative live census: "when did I first mention la roca", taught MATCH form.
		`SELECT source, id, session_id, mentioned_at, text, "database" FROM (
		    SELECT 'human' AS source, e.id AS id, e.session_id AS session_id,
		           e.human_timestamp AS mentioned_at, e.human_text AS text,
		           'plugin:roca-corpus' AS "database"
		    FROM (SELECT rowid AS fila FROM plugin_roca_corpus.documents_search
		          WHERE documents_search MATCH '{human_text} : ("la" "roca")') AS f
		    JOIN plugin_roca_corpus.exchanges AS e ON e.id = f.fila
		 ) AS mentions ORDER BY mentioned_at ASC LIMIT 1`,
	} {
		if _, err := gate.Validate(stmt); err != nil {
			t.Errorf("the gate rejects a legitimate plugin FTS read:\n%s\n-> %v", stmt, err)
		}
	}
	for _, stmt := range []string{
		`SELECT content FROM plugin_roca_corpus.memories WHERE content MATCH '"la"' LIMIT 1`,
		`SELECT content FROM plugin_roca_corpus.receipts_fts WHERE receipts_fts MATCH '"la"' LIMIT 1`,
		`SELECT body FROM plugin_records.documents_search WHERE documents_search MATCH '"la"' LIMIT 1`,
		`SELECT documents_search FROM plugin_records.records WHERE documents_search MATCH '"la"' LIMIT 1`,
		`SELECT documents_search FROM (SELECT documents_search FROM plugin_records.records) AS r WHERE documents_search MATCH '"la"' LIMIT 1`,
		`SELECT EXISTS(SELECT 1 FROM plugin_records.records WHERE documents_search MATCH '"la"') FROM plugin_roca_corpus.documents_search LIMIT 1`,
		`WITH documents_search AS (SELECT documents_search FROM plugin_records.records) SELECT documents_search FROM documents_search WHERE documents_search MATCH '"la"' LIMIT 1`,
		`SELECT * FROM plugin_roca_corpus.documents_search_data LIMIT 1`,
	} {
		if _, err := gate.Validate(stmt); err == nil {
			t.Fatalf("MATCH on a non-FTS5 table escaped the gate: %s", stmt)
		}
	}
}

// The shadow tables are the index's guts: binary blocks that are nobody's
// memory. They cannot be hidden by dropping them, because dropping them breaks
// the virtual table, so the authorization callback denies the read.
func TestTheGateDeniesTheIndexShadowTables(t *testing.T) {
	gate := open(t)
	for _, table := range []string{
		"memories_fts_data", "memories_fts_idx", "memories_fts_docsize",
		"memories_fts_config", "exchanges_fts_data",
	} {
		stmt := "SELECT * FROM " + table + " LIMIT 1"
		if _, err := gate.Validate(stmt); err == nil {
			t.Errorf("the gate lets %q be read, and those are the index's guts", table)
		}
	}
}

// The index state is the tool's internal state, not the fleet's memory:
// invisible, like ingest_file_state.
func TestTheGateDeniesTheInternalSearchTables(t *testing.T) {
	gate := open(t)
	for _, table := range []string{"search_state"} {
		stmt := "SELECT * FROM " + table + " LIMIT 1"
		_, err := gate.Validate(stmt)
		if err == nil {
			t.Errorf("the gate lets %q be read, and that is internal state", table)
			continue
		}
		if !strings.Contains(err.Error(), "no such table") {
			t.Errorf("the reason for denying %q is %q, want it to say it does not exist", table, err)
		}
	}
}

// The index being in view does not open the door to writing into it: inserting
// into an FTS5 table is how you talk to it (that is how a 'rebuild' is done), and
// that has to stay the indexer's business, never a question's.
func TestTheGateStillDeniesWritingToTheIndex(t *testing.T) {
	gate := open(t)
	for _, stmt := range []string{
		`INSERT INTO memories_fts(memories_fts) VALUES ('rebuild')`,
		`DELETE FROM memories_fts`,
	} {
		if _, err := gate.Validate(stmt); err == nil {
			t.Errorf("the gate lets a write run:\n%s", stmt)
		}
	}
}

func open(t *testing.T) *sqlgate.Gate {
	t.Helper()
	gate, err := sqlgate.Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { gate.Close() })
	return gate
}
