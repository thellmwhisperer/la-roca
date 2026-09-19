package rocaops

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	_ "modernc.org/sqlite"
)

func TestCompactMemoryIDsRestoresMemoryFTSBehavior(t *testing.T) {
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
		VALUES (?, 'discovery', 'before compaction', 'agent')`, int64(jsSafeInteger+1)); err != nil {
		db.Close()
		t.Fatal(err)
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
	defer db.Close()

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

func TestMemoryFTSTriggerSQLMatchesSchema(t *testing.T) {
	got := memoryFTSTriggerSQL(schema)
	if len(got) != 3 {
		t.Fatalf("trigger statements = %d, want 3", len(got))
	}
	for _, statement := range got {
		if !containsSchema(statement) {
			t.Fatalf("trigger is not taken from schema.sql:\n%s", statement)
		}
	}
}

func containsSchema(statement string) bool {
	return strings.Contains(schema, statement)
}
