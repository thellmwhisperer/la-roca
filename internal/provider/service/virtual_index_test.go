package service_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestVirtualIndexIsDeterministicCachedAndBounded(t *testing.T) {
	fixed := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	wantBlocks := []string{
		"source chatgpt-web 2022-2024: 1 ses / 2 exch — pre-agentic",
		"source claude-code 2026: 1 ses / 3 exch — agentic",
		"source unknown: 1 ses / 0 exch",
		"memory discovery/agent: 1 — last 2026-08-01",
		"memory pattern/human: 1 — last 2026-08-02",
		"corpus sessions: 3",
		"corpus exchanges: 5",
		"corpus thinking: 0",
		"corpus tool_uses: 0",
		"index fts memories: 2/2",
		"index fts exchanges: 5/5",
		"index fts thinking: 0/0",
		"index fts sessions: 3/3",
		"index vector: not installed",
		"health: 7 pass / 0 warn / 0 fail",
		"gap undated sessions: 1",
		"gap empty sources: cursor",
	}
	wantText := "# La Roca index\n" +
		"generated: 2026-08-18T00:00:00Z\n" +
		"budget: 593/8000 tokens\n\n" +
		strings.Join(wantBlocks, "\n") + "\n"

	svc, paths := serviceWithPaths(t)
	seedVirtualIndexFixture(t, svc)
	full, err := svc.VirtualIndex(t.Context(), service.VirtualIndexRequest{
		GeneratedAt: fixed, Refresh: true,
	})
	if err != nil {
		t.Fatalf("VirtualIndex: %v", err)
	}
	if !slices.Equal(full.Blocks, wantBlocks) {
		t.Fatalf("blocks = %#v\nwant %#v", full.Blocks, wantBlocks)
	}
	if full.Text != wantText {
		t.Fatalf("text = %q\nwant %q", full.Text, wantText)
	}
	if _, err := os.Stat(filepath.Join(paths.data, "virtual-index.json")); err != nil {
		t.Fatalf("cache was not written: %v", err)
	}

	if _, err := svc.DB().SQL().Exec(`
		INSERT INTO sessions (session_id, source_agent, started_at)
		VALUES ('chatgpt-extra', 'chatgpt-web', '2023-03-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	cached, err := svc.VirtualIndex(t.Context(), service.VirtualIndexRequest{GeneratedAt: fixed})
	if err != nil {
		t.Fatalf("cached VirtualIndex: %v", err)
	}
	if cached.Text != full.Text {
		t.Fatal("roca index served a stale regeneration instead of the cache")
	}

	if _, err := svc.Ingest(t.Context(), service.IngestRequest{}); err != nil {
		t.Fatalf("ingest refresh: %v", err)
	}
	refreshed, err := svc.VirtualIndex(t.Context(), service.VirtualIndexRequest{GeneratedAt: fixed})
	if err != nil {
		t.Fatalf("refreshed VirtualIndex: %v", err)
	}
	if !strings.Contains(strings.Join(refreshed.Blocks, "\n"),
		"source chatgpt-web 2022-2024: 2 ses / 2 exch — pre-agentic") {
		t.Fatalf("ingest did not regenerate the index:\n%s", strings.Join(refreshed.Blocks, "\n"))
	}

	tight, err := svc.VirtualIndex(t.Context(), service.VirtualIndexRequest{
		GeneratedAt: fixed, TokenBudget: 300, Refresh: true,
	})
	if err != nil {
		t.Fatalf("truncated VirtualIndex: %v", err)
	}
	if !tight.Truncated || tight.Omitted == 0 || !strings.Contains(tight.Text, "truncated:") {
		t.Fatalf("truncation was silent: %+v\n%s", tight, tight.Text)
	}
	if strings.Contains(tight.Text, wantBlocks[len(wantBlocks)-1]) {
		t.Fatalf("truncated index still carries the omitted tail:\n%s", tight.Text)
	}
	if _, err := svc.VirtualIndex(t.Context(), service.VirtualIndexRequest{
		GeneratedAt: fixed, TokenBudget: 1, Refresh: true,
	}); err == nil || !strings.Contains(err.Error(), "cannot fit the mandatory header") {
		t.Fatalf("undersized budget error = %v", err)
	}
}

func seedVirtualIndexFixture(t *testing.T, svc *service.Service) {
	t.Helper()
	db := svc.DB().SQL()
	statements := []string{
		`INSERT INTO sessions (session_id, source_agent, started_at, ended_at) VALUES
			('chatgpt-one', 'chatgpt-web', '2022-06-01T00:00:00Z', '2024-01-01T00:00:00Z'),
			('claude-one', 'claude-code', '2026-02-01T00:00:00Z', '2026-02-02T00:00:00Z'),
			('undated-one', '', NULL, NULL)`,
		`INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text) VALUES
			('chatgpt-one', 1, 'hello', 'hi'),
			('chatgpt-one', 2, 'later', 'ok'),
			('claude-one', 1, 'work', 'done'),
			('claude-one', 2, 'more', 'done'),
			('claude-one', 3, 'last', 'done')`,
		`INSERT INTO memories (layer, content, origin, created_at) VALUES
			('discovery', 'fixture discovery', 'agent', '2026-08-01T12:00:00Z'),
			('pattern', 'fixture pattern', 'human', '2026-08-02T12:00:00Z')`,
		`INSERT INTO ingest_file_state (path, source_kind, source_agent) VALUES
			('/tmp/cursor-empty', 'cursor', 'cursor')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}
