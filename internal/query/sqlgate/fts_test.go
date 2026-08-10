package sqlgate_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/query/sqlgate"
)

// The gate is worth its two halves, and both have to know the lexical index:
// the engine so it can prepare a MATCH, and the AST so it lets bm25 through.
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

// The shadow tables are the index's guts: binary blocks that are nobody's
// memory. They cannot be hidden by dropping them, because dropping them breaks
// the virtual table, so they are denied by name.
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
