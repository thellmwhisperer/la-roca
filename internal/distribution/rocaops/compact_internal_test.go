package rocaops

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	_ "modernc.org/sqlite"
)

func TestCompactMemoryIDsRestoresMemoryFTSBehavior(t *testing.T) {
	db := compactMemoryFixture(t, "before compaction", false)

	var id int64
	if err := db.QueryRow(`SELECT id FROM memories WHERE content = 'before compaction'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("compacted id = %d, want 1", id)
	}
	assertMemoryFTSCount(t, db, "before compaction", 1)

	if _, err := db.Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('discovery', 'after insert', 'agent')`); err != nil {
		t.Fatal(err)
	}
	assertMemoryFTSCount(t, db, "after insert", 1)

	if _, err := db.Exec(`UPDATE memories SET content = 'after update' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	assertMemoryFTSCount(t, db, "before compaction", 0)
	assertMemoryFTSCount(t, db, "after update", 1)

	if _, err := db.Exec(`DELETE FROM memories WHERE content = 'after update'`); err != nil {
		t.Fatal(err)
	}
	assertMemoryFTSCount(t, db, "after update", 0)
}

func TestCompactMemoryIDsClearsLegacySequenceWhenMemoriesAreEmpty(t *testing.T) {
	db := compactMemoryFixture(t, "legacy sequence", true)
	result, err := db.Exec(`INSERT INTO memories (layer, content, origin)
		VALUES ('discovery', 'after empty migration', 'agent')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("post-migration empty id = %d, want 1", id)
	}
}

func compactMemoryFixture(t *testing.T, content string, empty bool) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), DatabaseFilename)
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memories (id, layer, content, origin)
		VALUES (?, 'discovery', ?, 'agent')`, int64(jsSafeInteger+1), content); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if empty {
		if _, err := db.Exec(`DELETE FROM memories`); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := compactMemoryIDs(path); err != nil {
		t.Fatal(err)
	}
	db, err = bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func assertMemoryFTSCount(t *testing.T, db *sql.DB, term string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH ?`, term).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("FTS count for %q = %d, want %d", term, got, want)
	}
}
