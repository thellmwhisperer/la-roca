package incrementality_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	_ "modernc.org/sqlite"
)

func TestPublicPackageFingerprintsTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.jsonl")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	metadata, err := incrementality.MetadataFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "example", ParserVersion: "example-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fingerprint, metadata+":") ||
		!strings.HasSuffix(fingerprint, ":parser:example-v2") {
		t.Fatalf("target fingerprint = %q", fingerprint)
	}
}

func TestPublicPackageRecordsLoadsAndChecksState(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE ingest_file_state (
		path TEXT NOT NULL PRIMARY KEY,
		source_kind TEXT NOT NULL,
		source_agent TEXT,
		project TEXT,
		fingerprint TEXT,
		last_synced_at TEXT,
		last_error TEXT,
		metadata TEXT DEFAULT '{}'
	)`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	target := incrementality.Target{
		Path: "input.jsonl", Kind: "example", SourceAgent: "example-agent", Project: "demo",
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := incrementality.RecordState(ctx, tx, target, "5:10:digest", "",
		map[string]any{"records": 2}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	state, err := incrementality.LoadState(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !incrementality.Unchanged(state, target.Path, "5:10:digest") {
		t.Fatal("recorded fingerprint was not recognized as unchanged")
	}
	if !incrementality.UnchangedMetadata(state, target.Path, "5:10") {
		t.Fatal("recorded fingerprint did not preserve its metadata prefix")
	}
	var summary map[string]any
	if err := json.Unmarshal(state[target.Path].Metadata, &summary); err != nil {
		t.Fatal(err)
	}
	if summary["records"] != float64(2) {
		t.Fatalf("metadata = %s", state[target.Path].Metadata)
	}
}
