package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

func TestCursorIngestReportsCoverageAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "Library", "Application Support", "Cursor", "User",
		"globalStorage", "state.vscdb")
	copyCursorFixture(t, path)
	db := rocaDatabase(t)
	options := Options{Roots: Roots{Home: home}}

	first, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 || first.Scanned["cursor_database_files"] != 1 {
		t.Fatalf("Cursor coverage = errors:%d scanned:%v details:%+v",
			first.Errors, first.Scanned, first.ErrorDetails)
	}
	counts := first.Sources["cursor"]
	if counts == nil || counts.Sessions != 1 || counts.Exchanges != 2 ||
		counts.ThinkingBlocks != 1 || counts.ToolUses != 1 {
		t.Fatalf("Cursor counts = %+v", counts)
	}
	if first.RecordsExcluded != 4 || first.RecordsDiscarded != 0 {
		t.Fatalf("Cursor excluded/discarded = %d/%d: %+v",
			first.RecordsExcluded, first.RecordsDiscarded, first.DiscardSummary)
	}
	var model, surface string
	if err := db.SQL().QueryRow(`SELECT e.model, s.source_surface FROM exchanges e
		JOIN sessions s ON s.session_id = e.session_id
		WHERE e.session_id = 'cursor:11111111-2222-3333-4444-555555555555'
		AND e.exchange_number = 1`).Scan(&model, &surface); err != nil {
		t.Fatal(err)
	}
	if model != "fixture-cursor-model" || surface != "Cursor" {
		t.Fatalf("stored Cursor provenance = model %q, surface %q", model, surface)
	}
	var secondModel *string
	if err := db.SQL().QueryRow(`SELECT model FROM exchanges
		WHERE session_id = 'cursor:11111111-2222-3333-4444-555555555555'
		AND exchange_number = 2`).Scan(&secondModel); err != nil {
		t.Fatal(err)
	}
	if secondModel != nil {
		t.Fatalf("unrecorded Cursor model stored as %q", *secondModel)
	}

	second, err := Run(context.Background(), db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Delta != (Tables{}) || second.FilesRead != 0 || second.FilesSkipped != 1 {
		t.Fatalf("idempotent Cursor pass = delta:%+v read:%d skipped:%d",
			second.Delta, second.FilesRead, second.FilesSkipped)
	}
}

func TestCursorReaderSerializesAConsistentWALSnapshotWithoutChangingTheSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	copyCursorFixture(t, path)
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0`)
	exec(t, db, `INSERT INTO cursorDiskKV VALUES (
		'composerData:33333333-4444-5555-6666-777777777777',
		'{"composerId":"33333333-4444-5555-6666-777777777777","createdAt":1785589400000,
		  "lastUpdatedAt":1785589402000,"name":"Second synthetic session","status":"completed",
		  "unifiedMode":"chat","fullConversationHeadersOnly":[
		  {"bubbleId":"12121212-3434-5656-7878-909090909090","type":1},
		  {"bubbleId":"98989898-7676-5454-3232-101010101010","type":2}]}')`)
	exec(t, db, `INSERT INTO cursorDiskKV VALUES (
		'bubbleId:33333333-4444-5555-6666-777777777777:12121212-3434-5656-7878-909090909090',
		'{"bubbleId":"12121212-3434-5656-7878-909090909090","type":1,
		  "text":"ask the second synthetic question","createdAt":"2026-08-01T14:00:01Z",
		  "tokenCount":{"inputTokens":0,"outputTokens":0}}')`)
	exec(t, db, `INSERT INTO cursorDiskKV VALUES (
		'bubbleId:33333333-4444-5555-6666-777777777777:98989898-7676-5454-3232-101010101010',
		'{"bubbleId":"98989898-7676-5454-3232-101010101010","type":2,
		  "text":"answer the second synthetic question","createdAt":"2026-08-01T14:00:02Z",
		  "modelInfo":{"modelName":"fixture-cursor-model"},
		  "tokenCount":{"inputTokens":3,"outputTokens":2}}')`)

	mainBefore, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	walBefore, err := Fingerprint(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	records, complaints, err := ReadCursor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 0 || len(records.Sessions) != 2 {
		t.Fatalf("snapshot read = sessions:%d complaints:%v", len(records.Sessions), complaints)
	}
	mainAfter, _ := Fingerprint(path)
	walAfter, _ := Fingerprint(path + "-wal")
	if mainAfter != mainBefore || walAfter != walBefore {
		t.Fatal("reading Cursor changed its database or WAL")
	}
}

func copyCursorFixture(t *testing.T, target string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("parsers", "testdata", "conformance",
		"cursor-database", "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRegistrationDeclaresTheDatabaseStoreAndCorpusDestination(t *testing.T) {
	registered, ok := parsers.Lookup("cursor_database")
	if !ok {
		t.Fatal("Cursor database parser is not registered")
	}
	if registered.SourceAgent != "cursor" ||
		registered.CanonicalHarness != "Cursor" ||
		registered.Destination != parsers.DestinationCorpus || registered.Version == "" ||
		len(registered.Locations) != 3 {
		t.Fatalf("Cursor registration = %+v", registered)
	}
}
