package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

const harvestCursorSession = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func TestGrowingSessionAppendsWithoutVersioningOldRows(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(home, "w", "demo")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(home, ".claude", "projects", encodeRoot(workspace),
		harvestCursorSession+".jsonl")
	writeHarvestTranscript(t, sessionPath, workspace, 1)

	db := corpusDatabase(t)
	roots := ResolveRoots(Environment{GOOS: "darwin", Home: home},
		Settings{WorkspaceRoots: []string{filepath.Dir(workspace)}})
	options := Options{Roots: roots}
	ctx := context.Background()

	first, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Errors != 0 {
		t.Fatalf("first ingest errors=%+v", first.ErrorDetails)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 1 {
		t.Fatalf("after pass 1 exchanges = %d, want 1", got)
	}
	firstID := harvestExchangeID(t, db, 1)
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 1 wrote lineage for a first landing")
	}

	writeHarvestTranscript(t, sessionPath, workspace, 2)
	second, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Errors != 0 {
		t.Fatalf("second ingest errors=%+v", second.ErrorDetails)
	}
	if second.Delta.Exchanges != 1 {
		t.Fatalf("pass 2 delta exchanges = %d, want 1 appended", second.Delta.Exchanges)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 2 {
		t.Fatalf("after pass 2 exchanges = %d, want 2", got)
	}
	if harvestExchangeID(t, db, 1) != firstID {
		t.Fatal("pass 2 rewrote the already-ingested exchange")
	}
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 2 versioned an unchanged row")
	}

	writeHarvestTranscript(t, sessionPath, workspace, 3)
	third, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if third.Errors != 0 {
		t.Fatalf("third ingest errors=%+v", third.ErrorDetails)
	}
	if third.Delta.Exchanges != 1 {
		t.Fatalf("pass 3 delta exchanges = %d, want 1 appended", third.Delta.Exchanges)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 3 {
		t.Fatalf("after pass 3 exchanges = %d, want 3", got)
	}
	if harvestExchangeID(t, db, 1) != firstID {
		t.Fatal("pass 3 rewrote the already-ingested exchange")
	}
	if harvestVersionCount(t, db) != 0 {
		t.Fatalf("pass 3 versioned an unchanged row")
	}

	rewriteHarvestExchange(t, sessionPath, workspace)
	fourth, err := Run(ctx, db, registry(t), options)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Errors != 0 {
		t.Fatalf("rewrite ingest errors=%+v", fourth.ErrorDetails)
	}
	if got := countRows(t, db.SQL(), "exchanges"); got != 3 {
		t.Fatalf("after rewrite exchanges = %d, want 3 current rows", got)
	}
	if harvestVersionCount(t, db) != 1 {
		t.Fatalf("rewrite lineage rows = %d, want 1", harvestVersionCount(t, db))
	}
	assertLineageHasNoContent(t, db)
}

func corpusDatabase(t *testing.T) *store.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roca-corpus.db")
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatalf("corpus schema: %v", err)
	}
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeHarvestTranscript(t *testing.T, path, cwd string, exchanges int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := ""
	for turn := 1; turn <= exchanges; turn++ {
		body += harvestTurn(turn, cwd, fmt.Sprintf("question %d", turn),
			fmt.Sprintf("answer %d", turn))
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteHarvestExchange(t *testing.T, path, cwd string) {
	t.Helper()
	body := harvestTurn(1, cwd, "rewritten question", "rewritten answer")
	body += harvestTurn(2, cwd, "question 2", "answer 2")
	body += harvestTurn(3, cwd, "question 3", "answer 3")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func harvestTurn(turn int, cwd, question, answer string) string {
	stamp := fmt.Sprintf("2026-08-01T10:00:%02dZ", turn*10)
	return fmt.Sprintf("{\"type\":\"user\",\"timestamp\":%q,\"cwd\":%q,\"message\":{\"content\":%q}}\n"+
		"{\"type\":\"assistant\",\"timestamp\":%q,\"message\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n",
		stamp, cwd, question, stamp, answer)
}

func harvestExchangeID(t *testing.T, db *store.DB, number int) int64 {
	t.Helper()
	var id int64
	if err := db.SQL().QueryRow(`SELECT id FROM exchanges WHERE session_id = ? AND exchange_number = ?`,
		harvestCursorSession, number).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func harvestVersionCount(t *testing.T, db *store.DB) int {
	t.Helper()
	var count int
	err := db.SQL().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'exchange_versions'`).
		Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		return 0
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM exchange_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertLineageHasNoContent(t *testing.T, db *store.DB) {
	t.Helper()
	rows, err := db.SQL().Query(`SELECT name FROM pragma_table_info('exchange_versions')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		switch name {
		case "human_text", "agent_text", "full_text":
			t.Fatalf("lineage still stores content column %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
