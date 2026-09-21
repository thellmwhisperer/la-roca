package incrementality_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
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
	tagged, err := incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "example", ParserVersion: "example-v2", Machine: "hub",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !incrementality.IsMachinePromotion(fingerprint, tagged, "hub") {
		t.Fatalf("promotion not recognized: recorded=%q fingerprint=%q", fingerprint, tagged)
	}
	if incrementality.Unchanged(map[string]incrementality.FileState{
		path: {Fingerprint: fingerprint},
	}, path, tagged) {
		t.Fatal("Unchanged treated a machine promotion as an exact match")
	}
	if !incrementality.UnchangedMetadata(map[string]incrementality.FileState{
		path: {Fingerprint: fingerprint},
	}, path, metadata, "hub") {
		t.Fatal("machine-less metadata prefix was rejected after machine tagging")
	}
}

func TestMachinePromotionCases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("same\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "example", ParserVersion: "claude-session-v6",
	})
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "example", ParserVersion: "claude-session-v6", Machine: "hub",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := incrementality.TargetFingerprint(incrementality.Target{
		Path: path, Kind: "example", ParserVersion: "claude-session-v6", Machine: "hub",
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name                         string
		recorded, current, machine   string
		wantPromotion, wantUnchanged bool
	}{
		{name: "legacy fingerprint", recorded: legacy, current: tagged, machine: "hub",
			wantPromotion: true},
		{name: "machine-aware fingerprint", recorded: tagged, current: tagged, machine: "hub",
			wantUnchanged: true},
		{name: "changed content", recorded: legacy, current: changed, machine: "hub"},
		{name: "missing machine field", recorded: legacy, current: tagged, machine: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			gotPromotion := incrementality.IsMachinePromotion(testCase.recorded, testCase.current, testCase.machine)
			if gotPromotion != testCase.wantPromotion {
				t.Fatalf("IsMachinePromotion = %v, want %v (recorded=%q current=%q machine=%q)",
					gotPromotion, testCase.wantPromotion, testCase.recorded, testCase.current, testCase.machine)
			}
			gotUnchanged := incrementality.Unchanged(map[string]incrementality.FileState{
				path: {Fingerprint: testCase.recorded},
			}, path, testCase.current)
			if gotUnchanged != testCase.wantUnchanged {
				t.Fatalf("Unchanged = %v, want %v", gotUnchanged, testCase.wantUnchanged)
			}
		})
	}
}

func TestContentFingerprintFramesOrderedFields(t *testing.T) {
	joined := incrementality.ContentFingerprint("ab", "c")
	boundaryChanged := incrementality.ContentFingerprint("a", "bc")
	orderChanged := incrementality.ContentFingerprint("c", "ab")
	if joined == boundaryChanged || joined == orderChanged {
		t.Fatalf("content fingerprints collide: joined=%s boundary=%s order=%s",
			joined, boundaryChanged, orderChanged)
	}
	if joined != incrementality.ContentFingerprint("ab", "c") {
		t.Fatal("content fingerprint is not deterministic")
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

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = incrementality.RecordState(ctx, tx, target, "changed", "",
		map[string]any{"unsupported": make(chan struct{})})
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("unsupported metadata was accepted")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	state, err = incrementality.LoadState(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if state[target.Path].Fingerprint != "5:10:digest" {
		t.Fatalf("failed metadata encoding changed state to %q", state[target.Path].Fingerprint)
	}
}

func TestStandaloneModuleCompiles(t *testing.T) {
	command := exec.Command("go", "test", "-mod=readonly", ".")
	command.Dir = filepath.Join("testdata", "external")
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile standalone consumer module: %v\n%s", err, output)
	}
}
