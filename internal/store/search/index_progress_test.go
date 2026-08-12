package search_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// The build records each FTS table as it finishes, so a run that dies on a later
// table resumes from that table instead of rebuilding the ones that already
// completed. On the real database the exchanges rebuild is the expensive one and
// it holds the write lock while it runs, so redoing it is not free.
//
// There is no lease and no second builder protection here: two builders still
// converge, because an FTS `rebuild` is idempotent. What this removes is the
// wasted work after a failure.
func TestTheBuildResumesFromTheTableThatFailed(t *testing.T) {
	db, ctx := unbuiltIndex(t)

	// The last table cannot be rebuilt: a plain table of the same name survives
	// EnsureSearchSchema (its DDL is IF NOT EXISTS) and has no rebuild column.
	writeTo(t, db, `DROP TABLE sessions_fts`)
	writeTo(t, db, `CREATE TABLE sessions_fts (x)`)

	if _, err := search.Index(ctx, db, nil); err == nil {
		t.Fatal("the build reported success over a table it could not rebuild")
	}

	// What completed before the failure is recorded, and the terminal state is not.
	for _, table := range []string{"memories_fts", "exchanges_fts", "thinking_fts"} {
		if got := stateOf(t, db, "lexical_index:"+table); got != "built" {
			t.Errorf("%s completed and was not recorded: state = %q", table, got)
		}
	}
	if got := stateOf(t, db, "lexical_index"); got == "built" {
		t.Errorf("the terminal state claims a build that failed: %q", got)
	}
}

// The recorded tables are SKIPPED on the next run, which is the whole point. It is
// proved by dropping a table and marking it done: a build that still tried to
// rebuild it would fail, and one that honours the record does not.
func TestARecordedTableIsNotRebuiltAgain(t *testing.T) {
	db, ctx := unbuiltIndex(t)

	writeTo(t, db, `DROP TABLE memories_fts`)
	writeTo(t, db, `CREATE TABLE memories_fts (x)`)
	writeTo(t, db, `INSERT INTO search_state (key, value, updated_at)
		VALUES ('lexical_index:memories_fts', 'built', datetime('now'))`)

	if _, err := search.Index(ctx, db, nil); err != nil {
		t.Fatalf("a table already recorded as built was rebuilt anyway: %v", err)
	}
	if got := stateOf(t, db, "lexical_index"); got != "built" {
		t.Errorf("the build did not finish: terminal state = %q", got)
	}
}

func unbuiltIndex(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	db := seededWorld(t)
	ctx := context.Background()
	if _, err := search.EnsureTokenizer(ctx, db, nil); err != nil {
		t.Fatalf("EnsureTokenizer: %v", err)
	}
	writeTo(t, db, `DELETE FROM search_state
		WHERE key = 'lexical_index' OR key LIKE 'lexical_index:%'`)
	return db, ctx
}

func stateOf(t *testing.T, db *store.DB, key string) string {
	t.Helper()
	var value string
	err := db.SQL().QueryRow(`SELECT value FROM search_state WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return value
}
