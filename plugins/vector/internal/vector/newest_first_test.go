package vector

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCoreCLIWalksNewestFirstByTime(t *testing.T) {
	var statements []string
	core := CoreCLI{Executable: "/synthetic/roca", DBPath: "/synthetic/roca.db",
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			statement := args[len(args)-1]
			statements = append(statements, statement)
			return []byte(`{"rows":[]}`), nil
		}}
	if err := core.WalkSources(context.Background(), "", func(sourceRow) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(statements) != 4 {
		t.Fatalf("statements = %d", len(statements))
	}
	joined := strings.Join(statements, "\n")
	for _, want := range []string{
		"ORDER BY COALESCE(created_at,'') DESC",
		"ORDER BY COALESCE(occurred_at,'') DESC",
		"ORDER BY id DESC",
		"ORDER BY COALESCE(started_at,'') DESC",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing newest-first clause %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "ORDER BY id LIMIT") || strings.Contains(joined, "ORDER BY session_id LIMIT") {
		t.Fatalf("walk still pages oldest-first: %s", joined)
	}
}

func TestDeclaredCorpusPagesNewestFirst(t *testing.T) {
	corpus := DeclaredCorpus{Database: vectorDatabase{
		Alias:  "plugin_roca_corpus",
		Tables: []vectorTable{{Name: "memories", IDColumn: "id", TextColumns: []string{"content"}}},
	}}
	first := corpus.pageQuery(corpus.Database.Tables[0], "")
	if !strings.Contains(first, "ORDER BY source_id DESC") {
		t.Fatalf("declared walk is not newest-first: %s", first)
	}
	next := corpus.pageQuery(corpus.Database.Tables[0], "9")
	if !strings.Contains(next, "source_id<'9'") && !strings.Contains(next, "source_id<") {
		t.Fatalf("declared cursor is not descending: %s", next)
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
