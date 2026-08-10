package store_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// The full-text index is not part of the identity schema: it is a derived
// artefact. That is why its own function creates it and not ApplySchema, whose
// contract (exactly eight tables) measures precisely a Roca database's identity.
func TestEnsureSearchSchemaCreatesTheFullTextIndex(t *testing.T) {
	ctx := context.Background()
	db := withSchema(t)

	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSearchSchema: %v", err)
	}

	tables := tableNames(t, db.SQL())
	for _, want := range []string{
		"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts",
	} {
		if !slices.Contains(tables, want) {
			t.Errorf("the search table %q is missing; there are %v", want, tables)
		}
	}
}

func TestEnsureSearchSchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := withSchema(t)

	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("first EnsureSearchSchema: %v", err)
	}
	first := tableNames(t, db.SQL())
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("second EnsureSearchSchema: %v", err)
	}
	second := tableNames(t, db.SQL())

	if !slices.Equal(first, second) {
		t.Errorf("the second pass changed the tables:\n%v\n%v", first, second)
	}
}

// The index maintains itself. Without this, a memory stored after indexing
// would be invisible to search until the next full reindex, which is exactly the
// failure an incremental index exists to avoid.
func TestTheTriggersKeepTheIndexUpToDate(t *testing.T) {
	ctx := context.Background()
	db := withSchema(t)
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSearchSchema: %v", err)
	}

	write(t, db, `INSERT INTO memories (id, layer, content, origin)
		VALUES (1, 'fact', 'el capitán bendijo los guiones largos', 'human')`)
	if n := ftsMatches(t, db, "memories_fts", "guiones"); n != 1 {
		t.Errorf("after inserting, matches = %d, want 1", n)
	}

	// Without diacritics too: the index folds diacritics like the cascade does.
	if n := ftsMatches(t, db, "memories_fts", "capitan"); n != 1 {
		t.Errorf("searching without the diacritic, matches = %d, want 1", n)
	}

	write(t, db, `UPDATE memories SET content = 'ahora habla de binarios' WHERE id = 1`)
	if n := ftsMatches(t, db, "memories_fts", "guiones"); n != 0 {
		t.Errorf("after the update, the old text is still indexed (%d matches)", n)
	}
	if n := ftsMatches(t, db, "memories_fts", "binarios"); n != 1 {
		t.Errorf("after the update, the new text is not indexed (%d matches)", n)
	}

	write(t, db, `DELETE FROM memories WHERE id = 1`)
	if n := ftsMatches(t, db, "memories_fts", "binarios"); n != 0 {
		t.Errorf("after the delete, the row is still indexed (%d matches)", n)
	}
}

// The two columns of exchanges are indexed together, which is what allows a
// single query by term against both sides of the conversation.
func TestTheExchangesIndexCoversBothColumns(t *testing.T) {
	ctx := context.Background()
	db := withSchema(t)
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSearchSchema: %v", err)
	}

	write(t, db, `INSERT INTO exchanges (id, human_text, agent_text)
		VALUES (1, 'pregunta del humano sobre wallpapers', 'respuesta del agente sobre binarios')`)

	if n := ftsMatches(t, db, "exchanges_fts", "wallpapers"); n != 1 {
		t.Errorf("human_text not indexed (%d matches)", n)
	}
	if n := ftsMatches(t, db, "exchanges_fts", "binarios"); n != 1 {
		t.Errorf("agent_text not indexed (%d matches)", n)
	}
}

// An already indexed database has to stay "current": the index is a derived
// artefact, not a structural difference. If it counted as an orphan, every init
// over an indexed database would shout about tables it does not recognize.
func TestTheSearchIndexDoesNotPolluteAdoption(t *testing.T) {
	ctx := context.Background()
	db := withSchema(t)
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		t.Fatalf("EnsureSearchSchema: %v", err)
	}

	report, err := store.Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Verdict != store.VerdictCurrent {
		t.Errorf("verdict = %q (%s), want current", report.Verdict, report.Reason)
	}
	if len(report.Orphans) != 0 {
		t.Errorf("orphans = %v, want none: the index is a derived artefact",
			report.Orphans)
	}
}

// --- helpers ---

func withSchema(t *testing.T) *store.DB {
	t.Helper()
	db := openFresh(t)
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	return db
}

func write(t *testing.T, db *store.DB, stmt string) {
	t.Helper()
	err := db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(stmt)
		return err
	})
	if err != nil {
		t.Fatalf("write %q: %v", stmt, err)
	}
}

func ftsMatches(t *testing.T, db *store.DB, table, term string) int {
	t.Helper()
	var n int
	queryVec := "SELECT COUNT(*) FROM " + table + " WHERE " + table + " MATCH ?"
	if err := db.SQL().QueryRow(queryVec, term).Scan(&n); err != nil {
		t.Fatalf("count matches of %q in %s: %v", term, table, err)
	}
	return n
}
