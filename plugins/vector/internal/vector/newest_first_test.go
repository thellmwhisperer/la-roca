package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeclaredCorpusPagesNewestFirst(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/source.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE records (id TEXT PRIMARY KEY, body TEXT, occurred_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < walkPageSize+3; index++ {
		id := fmt.Sprintf("row-%04d", (index*137)%(walkPageSize+3))
		occurred := fmt.Sprintf("2026-08-%02dT%02d:%02d:%02dZ", 31-index/24, index%24, index%60, index%60)
		if index >= 31*24 {
			occurred = fmt.Sprintf("2025-07-%02dT00:00:00Z", 28-(index%28))
		}
		if _, err := db.Exec(`INSERT INTO records VALUES (?,?,?)`, id, "body "+id, occurred); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO records VALUES ('9','numeric recent','2027-01-01T00:00:00Z'),('10','numeric old','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	corpus := DeclaredCorpus{Core: CoreCLI{Executable: "sqlite-fixture", Run: sqliteRunner(db)},
		Database: vectorDatabase{Plugin: "fixture", Database: "records", Alias: "main",
			Tables: []vectorTable{{Name: "records", IDColumn: "id", TextColumns: []string{"body"},
				TimeColumns: []string{"occurred_at"}}}}}
	var rows []sourceRow
	if err := corpus.WalkSources(context.Background(), "records", func(row sourceRow) error {
		rows = append(rows, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != walkPageSize+5 {
		t.Fatalf("walk returned %d rows, want %d", len(rows), walkPageSize+5)
	}
	for index := 1; index < len(rows); index++ {
		if rows[index-1].occurredAt < rows[index].occurredAt {
			t.Fatalf("timestamps rose at %d: %q before %q", index, rows[index-1].occurredAt, rows[index].occurredAt)
		}
	}
	if rows[0].sourceID != "9" || rows[len(rows)-1].sourceID != "10" {
		t.Fatalf("time did not dominate non-monotonic IDs: first=%q last=%q", rows[0].sourceID, rows[len(rows)-1].sourceID)
	}
}

func sqliteRunner(db *sql.DB) CommandRunner {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		result, err := db.Query(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		defer result.Close()
		columns, err := result.Columns()
		if err != nil {
			return nil, err
		}
		rows := []map[string]any{}
		for result.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for index := range values {
				pointers[index] = &values[index]
			}
			if err := result.Scan(pointers...); err != nil {
				return nil, err
			}
			row := map[string]any{}
			for index, column := range columns {
				row[column] = values[index]
			}
			rows = append(rows, row)
		}
		return json.Marshal(map[string]any{"rows": rows})
	}
}

func TestNewestFirstIngestMakesRecentRowsQueryableWhileOlderWait(t *testing.T) {
	recent := sourceRow{kind: "memories", text: "alpha recent decision", createdAt: "2026-08-01", occurredAt: "2026-08-01"}
	older := sourceRow{kind: "memories", text: "gamma ancient note", createdAt: "2025-01-01", occurredAt: "2025-01-01"}
	gate := make(chan struct{})
	corpus := &gatedCorpus{recent: []sourceRow{recent}, older: []sourceRow{older}, proceed: gate}
	embedder := &recordingEmbedder{}
	index := Index{Corpus: corpus, VectorPath: t.TempDir() + "/vector.db",
		Model: DefaultModel, Embedder: embedder, BatchSize: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	var ingestErr error
	go func() {
		defer wg.Done()
		_, ingestErr = index.Ingest(ctx)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(embedder.snapshot()) >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(embedder.snapshot()) < 1 {
		close(gate)
		wg.Wait()
		t.Fatalf("recent source was not embedded first: %+v err=%v", embedder.inputs, ingestErr)
	}
	first := strings.Join(embedder.snapshot()[0], " ")
	if !strings.Contains(first, "alpha recent") {
		close(gate)
		wg.Wait()
		t.Fatalf("first embedded batch was not the recent source: %q", first)
	}
	results, err := index.Query(context.Background(), "alpha", 10)
	if err != nil {
		close(gate)
		wg.Wait()
		t.Fatal(err)
	}
	if len(results) == 0 || !strings.Contains(results[0].Text, "alpha recent") {
		close(gate)
		wg.Wait()
		t.Fatalf("recent row not queryable yet: %+v", results)
	}
	for _, result := range results {
		if strings.Contains(result.Text, "gamma ancient") {
			close(gate)
			wg.Wait()
			t.Fatalf("older row appeared before it was ingested: %+v", results)
		}
	}
	close(gate)
	wg.Wait()
	if ingestErr != nil {
		t.Fatal(ingestErr)
	}
}

type gatedCorpus struct {
	recent  []sourceRow
	older   []sourceRow
	proceed chan struct{}
}

func (g *gatedCorpus) WalkSources(ctx context.Context, sourceKind string, visit func(sourceRow) error) error {
	for _, source := range g.recent {
		if sourceKind != "" && source.kind != sourceKind {
			continue
		}
		if err := visit(source); err != nil {
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.proceed:
	}
	for _, source := range g.older {
		if sourceKind != "" && source.kind != sourceKind {
			continue
		}
		if err := visit(source); err != nil {
			return err
		}
	}
	return nil
}

func (g *gatedCorpus) ResolveSource(_ context.Context, kind string, where locator) (string, error) {
	for _, source := range append(append([]sourceRow{}, g.recent...), g.older...) {
		if source.kind == kind && source.locator().Identity == where.Identity {
			return source.text, nil
		}
	}
	return "", nil
}

func (e *recordingEmbedder) snapshot() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]string, len(e.inputs))
	copy(out, e.inputs)
	return out
}
