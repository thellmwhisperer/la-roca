package search_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
	"github.com/thellmwhisperer/la-roca/internal/provider/query/sqlgate"
	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestLexicalIndexSearchFindsWhatWasSeeded(t *testing.T) {
	engine, _ := indexedWorld(t)

	res, err := engine.Search(context.Background(), request("long+dashes", search.MethodFTS))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Provenance.Method != search.MethodFTS {
		t.Fatalf("method = %q (%s), want fts", res.Provenance.Method, res.Provenance.Reason)
	}
	if !anyRowContains(res.Rows, "long dashes") {
		t.Errorf("the lexical search did not find what was seeded; it brought %d rows: %v",
			len(res.Rows), texts(res.Rows))
	}
}

// The index folds diacritics just like the tokenizer does, so asking without
// diacritics finds what was written with them. It is what the LIKE did with its
// second folded variant, and here it comes free from the tokenizer.
func TestLexicalIndexSearchFoldsDiacritics(t *testing.T) {
	engine, _ := indexedWorld(t)

	res, err := engine.Search(context.Background(), request("muller", search.MethodFTS))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Rows) == 0 {
		t.Error("asking without the diacritic did not find what was written with it")
	}
}

// With no index at all, the search falls back to the usual LIKE. The engine does
// not run it: it says that is the method, and whoever compiles the template runs
// it.
func TestWithoutAnIndexItDegradesToLike(t *testing.T) {
	db := seededWorld(t)
	engine := &search.Engine{DB: db, Validate: theGate(t)}

	res, err := engine.Search(context.Background(), request("long+dashes", search.MethodFTS))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Provenance.Method != search.MethodLike {
		t.Errorf("method = %q, want it to degrade to like", res.Provenance.Method)
	}
	if res.Provenance.DegradedFrom != search.MethodFTS {
		t.Errorf("it does not say which method it degraded from: %+v", res.Provenance)
	}
}

// --- indexing ---

func TestIndexingTwiceRebuildsNothing(t *testing.T) {
	ctx := context.Background()
	db := seededWorld(t)

	first, err := search.Index(ctx, db)
	if err != nil {
		t.Fatalf("first indexing run: %v", err)
	}
	if !first.LexicalBuilt {
		t.Error("the first indexing run did not build the lexical index")
	}

	second, err := search.Index(ctx, db)
	if err != nil {
		t.Fatalf("second indexing run: %v", err)
	}
	if second.LexicalBuilt {
		t.Error("the second indexing run rebuilt the lexical index, which was already there")
	}
}

// A new row enters the lexical index immediately, without reindexing, because
// the triggers live in the database and fire on every write.
func TestANewRowEntersTheIndexImmediately(t *testing.T) {
	ctx := context.Background()
	engine, db := indexedWorld(t)

	writeTo(t, db, `INSERT INTO memories (layer, content, origin)
		VALUES ('fact', 'the whistling marmot watches the railway', 'human')`)

	res, err := engine.Search(ctx, request("marmot", search.MethodFTS))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !anyRowContains(res.Rows, "marmot") {
		t.Error("the lexical index did not see the freshly inserted row")
	}
}

// --- helpers ---

func request(term, method string) search.Request {
	plan := query.Plan{Template: query.TemplateSearchByTerm, Term: term, Limit: 10}
	stmt, _ := query.RenderSQLFTS(plan, nil, 10)
	return search.Request{Term: term, SQLLexical: stmt, Method: method, Limit: 10}
}

func seededWorld(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.ApplySchema(ctx, db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	writeTo(t, db, `
		INSERT INTO memories (layer, content, origin) VALUES
		  ('fact', 'the team forbids long dashes in every deliverable', 'human'),
		  ('fact', 'a naive Muller facade sketch from the design review', 'agent'),
		  ('fact', 'the database opens in WAL mode with a busy timeout', 'agent');
		INSERT INTO sessions (session_id, project, title) VALUES ('s1', 'roca', 'test session');
		INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text) VALUES
		  ('s1', 1, 'how do I configure the service startup', 'it is set with a yaml file'),
		  ('s1', 2, 'what about the long dashes', 'the team dislikes them');
		INSERT INTO thinking_blocks (session_id, exchange_number, full_text) VALUES
		  ('s1', 1, 'thinking about the long dashes and the format');`)
	return db
}

func indexedWorld(t *testing.T) (*search.Engine, *store.DB) {
	t.Helper()
	db := seededWorld(t)
	if _, err := search.Index(context.Background(), db); err != nil {
		t.Fatalf("Index: %v", err)
	}
	return &search.Engine{DB: db, Validate: theGate(t)}, db
}

func theGate(t *testing.T) func(string) (string, error) {
	t.Helper()
	gate, err := sqlgate.Open()
	if err != nil {
		t.Fatalf("Open the gate: %v", err)
	}
	t.Cleanup(func() { gate.Close() })
	return gate.Validate
}

func writeTo(t *testing.T, db *store.DB, stmt string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(stmt)
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func anyRowContains(rows []search.Row, text string) bool {
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Text), strings.ToLower(text)) {
			return true
		}
	}
	return false
}

func texts(rows []search.Row) []string {
	var out []string
	for _, row := range rows {
		out = append(out, row.Source+":"+row.Text)
	}
	return out
}

func rowWithID(rows []search.Row, id int64) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// THE FILTER THIS TEST EXISTS FOR.
//
// A memory another memory replaces stops answering, and the replacement
// answers. The row that carries `supersedes` is the replacement; the row it
// points at is the superseded one. Filtering on the row's own `supersedes`
// (IS NULL) was the inverted filter that hid the replacement and kept the old
// one answering, and this test pins the corrected filter at the store seam so
// it cannot come back.
func TestASupersededMemoryStopsAnswering(t *testing.T) {
	engine, db := indexedWorld(t)
	writeTo(t, db, `INSERT INTO memories (id, layer, content, origin)
		VALUES (100, 'fact', 'port alpha is eighty', 'human')`)
	writeTo(t, db, `INSERT INTO memories (id, layer, content, origin, supersedes)
		VALUES (101, 'fact', 'port alpha corrected is forty', 'human', 100)`)

	res, err := engine.Search(context.Background(), request("alpha", search.MethodFTS))
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !rowWithID(res.Rows, 101) {
		t.Errorf("the replacement memory does not answer: %v", texts(res.Rows))
	}
	if rowWithID(res.Rows, 100) {
		t.Errorf("the superseded memory still answers: %v", texts(res.Rows))
	}
}
