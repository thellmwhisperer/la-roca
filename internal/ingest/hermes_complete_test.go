package ingest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var hermesHandIngestedBlocks = []string{
	"The operator identity is a fictional cartographer based in the cobalt archipelago.",
	"Prefer short synthetic replies. Never invent a review finding.",
	"Run the invented compass check before launching a second probe.",
	"Prefer worktrees for every synthetic edit and keep the primary branch clean.",
	"GitHub work uses the invented gh fixture, never a browser extract.",
	"Slop gates stay a pattern: delete the old clone before adding a twin.",
	"The local memory stays on this machine; do not propose a cloud fallback.",
	"A moved path is not a stale change; verify the synthetic main first.",
	"Background work is confirmed before the next invented launch.",
}

func TestHermesCompleteSource(t *testing.T) {
	t.Run("absent home is a clean no-op", func(t *testing.T) {
		home := t.TempDir()
		roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
		plan := Scan(roots)
		if plan.Scanned["hermes_databases"] != 0 || plan.Scanned["hermes_files"] != 0 {
			t.Fatalf("absent Hermes was scanned: %+v", plan.Scanned)
		}
		if slices.Contains(plan.DetectedAgents, "hermes") {
			t.Fatalf("absent Hermes was detected: %v", plan.DetectedAgents)
		}
		db, result := runIngest(t, roots)
		if result.Errors != 0 || result.Sources["hermes"] != nil {
			t.Fatalf("absent Hermes was not a no-op: errors=%d sources=%+v",
				result.Errors, result.Sources)
		}
		if countRows(t, db.SQL(), "sessions") != 0 || countRows(t, db.SQL(), "memories") != 0 {
			t.Fatal("absent Hermes wrote rows")
		}
	})

	t.Run("channel usage routing and prompt stay with the session", func(t *testing.T) {
		home := t.TempDir()
		roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
		seedHermesIntel(t, roots)
		db, result := runIngest(t, roots)
		if result.Errors != 0 {
			t.Fatalf("errors = %d: %+v", result.Errors, result.ErrorDetails)
		}
		var surface, channel, model, promptHash, routeKey string
		var requests int
		if err := db.SQL().QueryRow(`
			SELECT COALESCE(source_surface, ''),
			       COALESCE(json_extract(metadata, '$.channel'), ''),
			       COALESCE(json_extract(metadata, '$.model_usage[0].model'), ''),
			       COALESCE(json_extract(metadata, '$.model_usage[0].requests'), 0),
			       COALESCE(json_extract(metadata, '$.system_prompt.hash'), ''),
			       COALESCE(json_extract(metadata, '$.routing[0].session_key'), '')
			FROM sessions WHERE session_id = 'h-intel'`).
			Scan(&surface, &channel, &model, &requests, &promptHash, &routeKey); err != nil {
			t.Fatal(err)
		}
		if surface != "Hermes/telegram" || channel != "telegram" {
			t.Errorf("channel = %q/%q, want Hermes/telegram", surface, channel)
		}
		if model != "fixture-hermes-model" || requests != 4 {
			t.Errorf("usage = %s/%d, want fixture-hermes-model/4", model, requests)
		}
		if promptHash != "prompt-fixture" || routeKey != "agent:main:telegram:dm:1" {
			t.Errorf("prompt/routing = %q/%q", promptHash, routeKey)
		}
		if got := countRows(t, db.SQL(), `memories WHERE content LIKE '%persona fixture%'`); got != 0 {
			t.Errorf("system prompt became corpus: %d", got)
		}
	})

	t.Run("named exclusions cover the unread Hermes stores", func(t *testing.T) {
		home := t.TempDir()
		roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
		seedHermesExclusions(t, roots)
		plan := Scan(roots)
		reasons := map[string]bool{}
		for _, target := range plan.Excluded {
			reasons[target.ExclusionReason] = true
		}
		for _, reason := range []string{
			"Hermes kanban is empty and unread",
			"Hermes sessions.db is empty and unread",
			"Hermes projects database is not conversation content",
			"Hermes cron executions are not conversation content",
			"Hermes verification evidence is not conversation content",
			"Hermes USER.md is not the curated MEMORY.md document",
			"Hermes memory lock file is not corpus content",
		} {
			if !reasons[reason] {
				t.Errorf("missing exclusion %q in %v", reason, keysOf(reasons))
			}
		}
		db, result := runIngest(t, roots)
		if result.Errors != 0 || result.FilesExcluded < 7 {
			t.Fatalf("exclusions = files %d errors %d summary %+v",
				result.FilesExcluded, result.Errors, result.DiscardSummary)
		}
		if countRows(t, db.SQL(), "sessions")+countRows(t, db.SQL(), "memories") != 0 {
			t.Fatal("named Hermes exclusions were ingested")
		}
	})

	t.Run("memory mutability and hand-ingested dedup", func(t *testing.T) {
		home := t.TempDir()
		roots := ResolveRoots(Environment{GOOS: "darwin", Home: home}, Settings{})
		memory := filepath.Join(roots.HermesHome, "memories", "MEMORY.md")
		world := &world{home: home}
		world.write(t, memory, strings.Join(hermesHandIngestedBlocks, "\n§\n"))

		db := rocaDatabase(t)
		for i, content := range hermesHandIngestedBlocks {
			exec(t, db.SQL(), `INSERT INTO memories (id, layer, content, metadata, origin)
				VALUES (?, 'pattern', ?, '{}', 'agent')`,
				int64(1152921504606847051)+int64(i), content)
		}
		opts := Options{Roots: roots}
		first, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if first.Sources["hermes"].MemoriesInserted != 0 {
			t.Fatalf("hand-ingested blocks were duplicated: %+v", first.Sources["hermes"])
		}
		if got := countRows(t, db.SQL(), `memories WHERE content IN (`+quotedList(hermesHandIngestedBlocks)+`)`); got != 9 {
			t.Fatalf("hand-ingested copies = %d, want 9", got)
		}

		second, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if second.Delta != (Tables{}) {
			t.Fatalf("unchanged rerun delta = %+v", second.Delta)
		}

		touchFuture(t, memory)
		third, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if third.Sources["hermes"].MemoriesInserted != 0 ||
			countRows(t, db.SQL(), `memories WHERE content IN (`+quotedList(hermesHandIngestedBlocks)+`)`) != 9 {
			t.Fatalf("touched rerun duplicated blocks: %+v", third.Sources["hermes"])
		}

		edited := append([]string(nil), hermesHandIngestedBlocks...)
		edited[2] = "Run the rewritten compass check after the first invented probe."
		world.write(t, memory, strings.Join(edited, "\n§\n"))
		fourth, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if fourth.Sources["hermes"].MemoriesInserted != 1 {
			t.Fatalf("edited block was not a new memory: %+v", fourth.Sources["hermes"])
		}
		if got := countRows(t, db.SQL(),
			`memories WHERE content = '`+hermesHandIngestedBlocks[2]+`' AND status = 'resolved'`); got != 1 {
			t.Fatalf("vanished edited block was not superseded: %d", got)
		}

		removed := edited[:len(edited)-1]
		world.write(t, memory, strings.Join(removed, "\n§\n"))
		fifth, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if fifth.Sources["hermes"].MemoriesInserted != 0 ||
			countRows(t, db.SQL(), `memories WHERE content = '`+edited[len(edited)-1]+`' AND status = 'resolved'`) != 1 {
			t.Fatalf("removed block was not superseded: %+v", fifth.Sources["hermes"])
		}

		rewritten := []string{
			"The operator identity now lives in a second invented harbour.",
			"Prefer a quieter synthetic review voice.",
		}
		world.write(t, memory, strings.Join(rewritten, "\n§\n"))
		sixth, err := Run(context.Background(), db, registry(t), opts)
		if err != nil {
			t.Fatal(err)
		}
		if sixth.Sources["hermes"].MemoriesInserted != 2 {
			t.Fatalf("rewritten file did not insert the new blocks: %+v", sixth.Sources["hermes"])
		}
		if got := countRows(t, db.SQL(), `memories WHERE source_agent = 'hermes' AND status = 'active'`); got != 2 {
			t.Fatalf("active Hermes memories after rewrite = %d, want 2", got)
		}
	})
}

func seedHermesIntel(t *testing.T, roots Roots) {
	t.Helper()
	db := openSynthetic(t, roots.HermesDB)
	defer db.Close()
	exec(t, db, `CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT,
	              title TEXT, started_at REAL, ended_at REAL, session_key TEXT,
	              system_prompt_hash TEXT, billing_provider TEXT)`)
	exec(t, db, `CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT,
	              content TEXT, timestamp REAL, active INTEGER)`)
	exec(t, db, `CREATE TABLE session_model_usage (session_id TEXT, model TEXT,
	              billing_provider TEXT, billing_base_url TEXT, api_call_count INTEGER,
	              input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
	              estimated_cost_usd REAL, cost_source TEXT)`)
	exec(t, db, `CREATE TABLE gateway_routing (scope TEXT, session_key TEXT, entry_json TEXT,
	              updated_at REAL)`)
	exec(t, db, `CREATE TABLE system_prompts (hash TEXT PRIMARY KEY, prompt TEXT)`)
	exec(t, db, `INSERT INTO sessions VALUES ('h-intel', 'telegram', 'fixture-hermes-model',
	              '/synthetic/demo', 'intel', 1785542400, 1785542700,
	              'agent:main:telegram:dm:1', 'prompt-fixture', 'fixture-provider')`)
	exec(t, db, `INSERT INTO messages VALUES (1, 'h-intel', 'user', 'count the invented tokens',
	              1785542400, 1)`)
	exec(t, db, `INSERT INTO messages VALUES (2, 'h-intel', 'assistant', 'four', 1785542401, 1)`)
	exec(t, db, `INSERT INTO session_model_usage VALUES ('h-intel', 'fixture-hermes-model',
	              'fixture-provider', 'https://fixture.example/v1', 4, 80, 20, 10, 0.25,
	              'fixture_pricing')`)
	entry, _ := json.Marshal(map[string]any{
		"session_key": "agent:main:telegram:dm:1", "session_id": "h-intel",
	})
	exec(t, db, `INSERT INTO gateway_routing VALUES ('/synthetic/hermes/sessions',
	              'agent:main:telegram:dm:1', ?, 1785542700)`, string(entry))
	exec(t, db, `INSERT INTO system_prompts VALUES ('prompt-fixture',
	              'You are the invented persona fixture for this session.')`)
}

func seedHermesExclusions(t *testing.T, roots Roots) {
	t.Helper()
	world := &world{home: roots.Home}
	world.write(t, filepath.Join(roots.HermesHome, "kanban.db"), "")
	world.write(t, filepath.Join(roots.HermesHome, "sessions.db"), "")
	world.write(t, filepath.Join(roots.HermesHome, "projects.db"), "")
	world.write(t, filepath.Join(roots.HermesHome, "verification_evidence.db"), "")
	world.write(t, filepath.Join(roots.HermesHome, "cron", "executions.db"), "")
	world.write(t, filepath.Join(roots.HermesHome, "memories", "USER.md"),
		"A synthetic user profile that must stay excluded.\n")
	world.write(t, filepath.Join(roots.HermesHome, "memories", "MEMORY.md.lock"), "")
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return strings.Join(quoted, ",")
}

func keysOf(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
