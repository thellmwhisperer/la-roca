package rocacorpus_test

import (
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"testing"
)

func TestCorpusChangeFrontierUpgrade(t *testing.T) {
	db, path := openCorpusDB(t)
	defer db.Close()
	for _, table := range []string{"sessions", "exchanges", "thinking_blocks", "memories"} {
		for _, event := range []string{"insert", "update", "delete"} {
			if _, err := db.Exec("DROP TRIGGER vector_changes_" + table + "_" + event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Exec(`DROP TABLE vector_changes; UPDATE plugin_schema SET schema_version=8;
 INSERT INTO ingest_file_state(path,source_kind) VALUES ('fixture','fixture');`); err != nil {
		t.Fatal(err)
	}
	t.Setenv(bundledplugin.EnvAllowHomeMigrate, "1")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	var before, after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vector_changes`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_id,title) VALUES ('frontier','Original title');
 INSERT INTO exchanges(session_id,human_text,agent_text) VALUES ('frontier','Question','Answer');
 UPDATE ingest_file_state SET fingerprint='different';
 UPDATE sessions SET ended_at='2026-09-20' WHERE session_id='frontier';`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vector_changes`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after-before != 2 {
		t.Fatalf("insert and metadata events=%d, want 2", after-before)
	}
	before = after
	if _, err := db.Exec(`UPDATE sessions SET title='Corrected title' WHERE session_id='frontier'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(DISTINCT source_kind) FROM vector_changes WHERE sequence>?`, before).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 2 {
		t.Fatalf("context affected kinds=%d, want session and exchange", after)
	}
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
}
