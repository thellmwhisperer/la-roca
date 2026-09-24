package opsvector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	_ "modernc.org/sqlite"
)

const legacyID = "1152921504606853945"

func TestRemapLegacyIDsRewritesSidecarSourceAndLocator(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "roca-ops.db")
	writeDB(t, ops, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY, legacy_id INTEGER, content TEXT);
		INSERT INTO memories VALUES (4, `+legacyID+`, 'handoff del CoS');`)
	sidecar := SidecarPath(ops)
	writeDB(t, sidecar, `
		CREATE TABLE chunks(
			id INTEGER PRIMARY KEY,
			source_kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			text_column TEXT NOT NULL DEFAULT '',
			chunk_index INTEGER NOT NULL,
			fingerprint TEXT NOT NULL,
			locator TEXT NOT NULL,
			UNIQUE(source_kind, source_id, text_column, chunk_index));
		CREATE TABLE sources(
			source_kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			raw_source_id TEXT NOT NULL,
			source_fingerprint TEXT NOT NULL,
			chunk_count INTEGER NOT NULL,
			PRIMARY KEY(source_kind, source_id));
		INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('memories','memories/`+legacyID+`','content',0,'fp',
			'{"source_id":"`+legacyID+`","identity":"old"}');
		INSERT INTO sources VALUES('memories','memories/`+legacyID+`','`+legacyID+`','src',1);`)

	stale, err := HasStaleLegacyIDs(t.Context(), ops)
	if err != nil || !stale {
		t.Fatalf("stale before remap = %v, %v", stale, err)
	}
	changed, err := RemapLegacyIDs(ops)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("remapped chunks = %d, want 1", changed)
	}
	stale, err = HasStaleLegacyIDs(t.Context(), ops)
	if err != nil || stale {
		t.Fatalf("stale after remap = %v, %v", stale, err)
	}

	db := openTest(t, sidecar)
	defer db.Close()
	var sourceID, locator string
	if err := db.QueryRow(`SELECT source_id, locator FROM chunks`).Scan(&sourceID, &locator); err != nil {
		t.Fatal(err)
	}
	if sourceID != "memories/4" {
		t.Fatalf("chunk source_id = %q, want memories/4", sourceID)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(locator), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["source_id"] != "4" {
		t.Fatalf("locator source_id = %#v, want 4", decoded["source_id"])
	}
	wantIdentity := incrementality.ContentFingerprint("memories\x004")
	if decoded["identity"] != wantIdentity {
		t.Fatalf("locator identity = %#v, want %s", decoded["identity"], wantIdentity)
	}
	var raw string
	if err := db.QueryRow(`SELECT raw_source_id FROM sources`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "4" {
		t.Fatalf("source raw_source_id = %q, want 4", raw)
	}
}

func TestRemapLegacyIDsPreservesDuplicatesForVectorCleanup(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "roca-ops.db")
	writeDB(t, ops, `
		CREATE TABLE memories(id INTEGER PRIMARY KEY, legacy_id INTEGER, content TEXT);
		INSERT INTO memories VALUES (1, `+legacyID+`, 'already remapped');`)
	sidecar := SidecarPath(ops)
	writeDB(t, sidecar, `
		CREATE TABLE chunks(
			id INTEGER PRIMARY KEY,
			source_kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			text_column TEXT NOT NULL DEFAULT '',
			chunk_index INTEGER NOT NULL,
			fingerprint TEXT NOT NULL,
			locator TEXT NOT NULL,
			UNIQUE(source_kind, source_id, text_column, chunk_index));
		INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('memories','memories/1','content',0,'new','{}');
		INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
			VALUES('memories','memories/`+legacyID+`','content',0,'old','{}');`)

	changed, err := RemapLegacyIDs(ops)
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("remapped duplicate chunks = %d, want 0", changed)
	}
	db := openTest(t, sidecar)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("chunks before vector cleanup = %d, want 2", n)
	}
}

func TestRemapLegacyIDsIsANoOpWithoutSidecarOrLegacyColumn(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.db")
	if n, err := RemapLegacyIDs(missing); err != nil || n != 0 {
		t.Fatalf("missing source = %d, %v", n, err)
	}
	ops := filepath.Join(root, "roca-ops.db")
	writeDB(t, ops, `CREATE TABLE memories(id INTEGER PRIMARY KEY, content TEXT);`)
	if n, err := RemapLegacyIDs(ops); err != nil || n != 0 {
		t.Fatalf("no sidecar = %d, %v", n, err)
	}
}

func writeDB(t *testing.T, path, schema string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func openTest(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHasStaleLegacyIDsHonorsCancellation(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "roca-ops.db")
	writeDB(t, ops, `CREATE TABLE memories(id INTEGER PRIMARY KEY, legacy_id INTEGER)`)
	writeDB(t, SidecarPath(ops), `CREATE TABLE chunks(id INTEGER PRIMARY KEY, source_kind TEXT, source_id TEXT)`)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := HasStaleLegacyIDs(ctx, ops); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled status = %v", err)
	}
}

func TestLegacyIDsAcrossLargeRepairedIndex(t *testing.T) {
	ops := filepath.Join(t.TempDir(), "roca-ops.db")
	writeDB(t, ops, `CREATE TABLE memories(id INTEGER PRIMARY KEY, legacy_id INTEGER);
		WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<16000)
		INSERT INTO memories SELECT n, 1152921504606846976+n FROM ids;`)
	writeDB(t, SidecarPath(ops), `CREATE TABLE chunks(id INTEGER PRIMARY KEY, source_kind TEXT, source_id TEXT, locator TEXT NOT NULL DEFAULT '{}');
		CREATE INDEX chunk_sources ON chunks(source_kind, source_id);
		WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<16000)
		INSERT INTO chunks(id, source_kind, source_id) SELECT n, 'memories', 'memories/' || n FROM ids;`)
	if stale, err := HasStaleLegacyIDs(t.Context(), ops); err != nil || stale {
		t.Fatalf("repaired index: stale=%v err=%v", stale, err)
	}
	if n, err := RemapLegacyIDs(ops); err != nil || n != 0 {
		t.Fatalf("repaired index: remapped=%d err=%v", n, err)
	}
	db := openTest(t, SidecarPath(ops))
	if _, err := db.Exec(`UPDATE chunks SET source_id = 'memories/1152921504606862976' WHERE id = 16000`); err != nil {
		t.Fatal(err)
	}
	if stale, err := HasStaleLegacyIDs(t.Context(), ops); err != nil || !stale {
		t.Fatalf("last legacy chunk: stale=%v err=%v", stale, err)
	}
	if n, err := RemapLegacyIDs(ops); err != nil || n != 1 {
		t.Fatalf("last legacy chunk: remapped=%d err=%v", n, err)
	}
	if stale, err := HasStaleLegacyIDs(t.Context(), ops); err != nil || stale {
		t.Fatalf("after repair: stale=%v err=%v", stale, err)
	}
}
