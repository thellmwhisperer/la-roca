package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestCursorIngestReportsCoverageAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "Library", "Application Support", "Cursor", "User",
		"globalStorage", "state.vscdb")
	writeFixture(t, path, filepath.Join("..", "..", "pkg", "parsers", "testdata", "conformance",
		"cursor-database", "state.vscdb"))
	first, second, db := ingestCursorHomeTwice(t, home)
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
	if second.Delta != (Tables{}) || second.FilesRead != 0 || second.FilesSkipped != 1 {
		t.Fatalf("idempotent Cursor pass = delta:%+v read:%d skipped:%d",
			second.Delta, second.FilesRead, second.FilesSkipped)
	}
}

func TestCursorReaderSerializesAConsistentWALSnapshotWithoutChangingTheSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `CREATE TABLE unrelated (id INTEGER PRIMARY KEY)`)
	exec(t, db, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0`)
	exec(t, db, `CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`)
	exec(t, db, `CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`)
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
	mainOnly, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := parsers.Lookup("cursor_database")
	if !ok {
		t.Fatal("Cursor database parser is not registered")
	}
	if registered.Parser.Detect(parsers.File{Content: mainOnly, Meta: parsers.FileMeta{
		FileName: "state.vscdb", SourceAgent: "cursor",
	}}) {
		t.Fatal("Cursor markers escaped the WAL into the main database")
	}
	records, complaints, err := ReadCursor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(complaints) != 0 || len(records.Sessions) != 1 {
		t.Fatalf("snapshot read = sessions:%d complaints:%v", len(records.Sessions), complaints)
	}
	mainAfter, _ := Fingerprint(path)
	walAfter, _ := Fingerprint(path + "-wal")
	if mainAfter != mainBefore || walAfter != walBefore {
		t.Fatal("reading Cursor changed its database or WAL")
	}
}

func TestCursorWorkspaceScanTargetsOnlyStateDatabases(t *testing.T) {
	home := t.TempDir()
	hash := filepath.Join(home, "Library", "Application Support", "Cursor", "User",
		"workspaceStorage", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if err := os.MkdirAll(hash, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"state.vscdb", "state.vscdb-wal", "state.vscdb-shm", "workspace.json",
	} {
		if err := os.WriteFile(filepath.Join(hash, name), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	registered, ok := parsers.Lookup("cursor_database")
	if !ok {
		t.Fatal("Cursor database parser is not registered")
	}
	plan := Plan{Scanned: map[string]int{}}
	addRegisteredParsers(Roots{Home: home}, &plan, []parsers.Registration{registered})

	if len(plan.Targets) != 1 || plan.Targets[0].Path != filepath.Join(hash, "state.vscdb") ||
		plan.Targets[0].FileName != "state.vscdb" {
		t.Fatalf("Cursor workspace targets = %+v", plan.Targets)
	}
	if plan.Scanned["cursor_database_files"] != 1 {
		t.Fatalf("Cursor workspace coverage = %+v", plan.Scanned)
	}
	if len(plan.DetectedAgents) != 1 || plan.DetectedAgents[0] != "cursor" {
		t.Fatalf("Cursor was not detected: %+v", plan.DetectedAgents)
	}
}

func TestCursorReaderRejectsForeignCandidatesByDesign(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		fileName string
		content  []byte
	}{
		{"a foreign filename", "workspace.json", []byte("synthetic")},
		{"non-SQLite content", "state.vscdb", []byte("not a sqlite database")},
		{"a truncated database", "state.vscdb", []byte("SQLite format")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), testCase.fileName)
			if err := os.WriteFile(path, testCase.content, 0o600); err != nil {
				t.Fatal(err)
			}
			records, complaints, err := ReadCursor(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if len(complaints) != 0 || len(records.Sessions) != 0 || len(records.Discards) != 1 {
				t.Fatalf("foreign Cursor candidate = sessions:%d discards:%d complaints:%v",
					len(records.Sessions), len(records.Discards), complaints)
			}
			if discard := records.Discards[0]; !discard.ByDesign ||
				discard.Reason != "file is not claimed by the registered parser" {
				t.Fatalf("foreign Cursor candidate discard = %+v", discard)
			}
		})
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

func TestCursorStoreIngestReportsCoverageAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	session := filepath.Join(home, ".cursor", "chats",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "11111111-aaaa-4bbb-8ccc-222222222222")
	writeFixture(t, filepath.Join(session, "store.db"), filepath.Join("..", "..", "pkg", "parsers",
		"testdata", "conformance", "cursor-store", "store.db"))
	writeFixture(t, filepath.Join(session, "meta.json"), filepath.Join("..", "..", "pkg", "parsers",
		"testdata", "conformance", "cursor-store", "meta.json"))
	first, second, db := ingestCursorHomeTwice(t, home)
	if got := first.Scanned["cursor_store_files"]; first.Errors != 0 || got != 1 {
		t.Fatalf("store files scanned = %d errors = %d details = %+v",
			got, first.Errors, first.ErrorDetails)
	}
	got := first.Sources["cursor"]
	if got == nil {
		t.Fatal("missing cursor source counts")
	}
	if got.Sessions != 1 || got.Exchanges != 2 || got.ThinkingBlocks != 1 || got.ToolUses != 1 ||
		first.RecordsExcluded != 2 || first.RecordsDiscarded != 0 {
		t.Fatalf("store yield = %+v excluded = %d discarded = %d",
			got, first.RecordsExcluded, first.RecordsDiscarded)
	}
	var model sql.NullString
	var surface, project, title string
	if err := db.SQL().QueryRow(`SELECT e.model, s.source_surface, s.project, s.title
		FROM exchanges e JOIN sessions s ON s.session_id = e.session_id
		WHERE e.session_id = 'cursor:11111111-aaaa-4bbb-8ccc-222222222222'
		AND e.exchange_number = 1`).Scan(&model, &surface, &project, &title); err != nil {
		t.Fatal(err)
	}
	if model.Valid || surface != "Cursor" ||
		project != "harbor" || title != "Synthetic harbor session" {
		t.Fatalf("stored Cursor store provenance = model %q surface %q project %q title %q",
			model.String, surface, project, title)
	}
	if second.Delta != (Tables{}) || second.FilesRead != 0 || second.FilesSkipped != 1 {
		t.Fatalf("idempotent Cursor store pass = delta:%+v read:%d skipped:%d",
			second.Delta, second.FilesRead, second.FilesSkipped)
	}
}

func TestCursorStoreScanTargetsOnlyStoreDatabasesAndPairsMeta(t *testing.T) {
	home := t.TempDir()
	session := filepath.Join(home, ".cursor", "chats",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "11111111-aaaa-4bbb-8ccc-222222222222")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"store.db", "store.db-wal", "store.db-shm", "meta.json", "prompt_history.json"} {
		if err := os.WriteFile(filepath.Join(session, name), []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, ok := parsers.Lookup(string(parsers.KindCursorStore))
	if !ok {
		t.Fatal("missing cursor_store registration")
	}
	plan := Plan{Scanned: map[string]int{}}
	addRegisteredParsers(Roots{Home: home}, &plan, []parsers.Registration{store})
	if len(plan.Targets) != 1 {
		t.Fatalf("store targets = %d, want only store.db: %+v", len(plan.Targets), plan.Targets)
	}
	target := plan.Targets[0]
	if target.FileName != "store.db" || target.SidecarPath == "" ||
		target.SessionID != "11111111-aaaa-4bbb-8ccc-222222222222" {
		t.Fatalf("store target pairing = %+v", target)
	}
	if plan.Scanned["cursor_store_files"] != 1 || len(plan.DetectedAgents) != 1 {
		t.Fatalf("store scan = scanned:%v agents:%v", plan.Scanned, plan.DetectedAgents)
	}
}

func ingestCursorHomeTwice(t *testing.T, home string) (first, second Result, db Database) {
	t.Helper()
	db = rocaDatabase(t)
	opts := Options{Roots: Roots{Home: home}}
	var err error
	first, err = Run(context.Background(), db, registry(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err = Run(context.Background(), db, registry(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	return first, second, db
}

func TestCursorStoreRegistrationDeclaresTheAgentHomeAndCorpusDestination(t *testing.T) {
	registered, ok := parsers.Lookup("cursor_store")
	if !ok {
		t.Fatal("Cursor store parser is not registered")
	}
	if registered.SourceAgent != "cursor" || registered.CanonicalHarness != "Cursor" ||
		registered.Destination != parsers.DestinationCorpus || registered.FileName != "store.db" {
		t.Fatalf("Cursor store registration = %+v", registered)
	}
}
