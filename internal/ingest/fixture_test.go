package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/layers"
	"github.com/thellmwhisperer/la-roca/internal/store"
	_ "modernc.org/sqlite"
)

// A whole synthetic world: every source of the v1 matrix is invented here. No
// real transcript, database or private vocabulary enters a test.

// world is a sandbox HOME with the agents' artefacts in it.
type world struct {
	home      string
	workspace string
	export    string
	env       Environment
	settings  Settings
}

func newWorld(t *testing.T) *world {
	t.Helper()
	home := t.TempDir()
	workspace := filepath.Join(home, "w")
	w := &world{
		home:      home,
		workspace: workspace,
		export:    filepath.Join(home, "explicit-anthropic-export"),
		env:       Environment{GOOS: "darwin", Home: home},
		settings:  Settings{WorkspaceRoots: []string{workspace}},
	}
	w.seed(t)
	return w
}

func (w *world) roots() Roots {
	roots := ResolveRoots(w.env, w.settings)
	roots.ClaudeWebExports = []string{w.export}
	return roots
}

// seededWorld is the common preamble of the tests that measure a second reach
// over the same disk: a fresh database with the synthetic world already
// ingested. The preamble is written once so the slop gate does not count a copy
// per test that needs it.
func seededWorld(t *testing.T) (*world, *store.DB, context.Context, Options) {
	t.Helper()
	w := newWorld(t)
	db := rocaDatabase(t)
	ctx := context.Background()
	options := Options{Roots: w.roots()}
	if _, err := Run(ctx, db, registry(t), options); err != nil {
		t.Fatalf("first run: %v", err)
	}
	return w, db, ctx, options
}

const cwdFixtureSessionID = "99999999-8888-7777-6666-555555555555"

// claudeMemoryFixture writes one synthetic Claude memory under the encoded
// directory of cwd and returns its path.
func claudeMemoryFixture(t *testing.T, world *world, roots Roots, cwd string) string {
	t.Helper()
	memory := filepath.Join(roots.ClaudeProjects, encodeRoot(cwd), "memory", "fact.md")
	world.write(t, memory, "---\nname: fact\ntype: project\n---\nA synthetic attributed fact.\n")
	return memory
}

// claudeSessionFixture writes one synthetic Claude transcript attributed to cwd.
func claudeSessionFixture(t *testing.T, world *world, roots Roots, cwd string) {
	t.Helper()
	world.write(t, filepath.Join(roots.ClaudeProjects, encodeRoot(cwd), cwdFixtureSessionID+".jsonl"),
		fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"question\"}}\n"+
			"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:01Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}}\n", cwd))
}

// claudeConfigFixture maps cwd in the Claude config so a normal ingest has a
// project mapping to attribute memories against.
func claudeConfigFixture(t *testing.T, world *world, roots Roots, cwd string) {
	t.Helper()
	world.write(t, roots.ClaudeConfig, `{"projects":{"`+cwd+`":{}}}`)
}

// claudeAttributedMemoryFixture writes a synthetic Claude memory and a config
// mapping for the same cwd, returning the memory path.
func claudeAttributedMemoryFixture(t *testing.T, world *world, roots Roots, cwd string) string {
	t.Helper()
	memory := claudeMemoryFixture(t, world, roots, cwd)
	claudeConfigFixture(t, world, roots, cwd)
	return memory
}

// projectDir is the directory name an agent would have encoded the demo project's
// working directory as, so the project decodes against the declared root.
func (w *world) projectDir() string { return encodeRoot(w.demoCwd()) }

func (w *world) demoCwd() string { return filepath.Join(w.workspace, "demo") }

func (w *world) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

const fixtureSessionID = "11111111-2222-3333-4444-555555555555"

func (w *world) seed(t *testing.T) {
	t.Helper()
	roots := w.roots()
	project := filepath.Join(roots.ClaudeProjects, w.projectDir())
	w.write(t, filepath.Join(roots.ClaudeWebExports[0], "conversations.json"), `[{
  "uuid":"web-fixture-1","name":"Synthetic web fixture","summary":"Invented export data.",
  "created_at":"2026-08-01T08:00:00Z","updated_at":"2026-08-01T08:00:02Z",
  "chat_messages":[
    {"uuid":"web-human-1","text":"name the fixture","sender":"human","created_at":"2026-08-01T08:00:01Z","parent_message_uuid":null},
    {"uuid":"web-agent-1","text":"glass beacon","sender":"assistant","created_at":"2026-08-01T08:00:02Z","parent_message_uuid":"web-human-1"}
  ]
}]`)
	w.write(t, filepath.Join(roots.ClaudeWebExports[0], "memories.json"),
		`[{"uuid":"web-memory-1","memory":"The synthetic export is explicitly declared.","created_at":"2026-08-01T07:00:00Z"}]`)

	// Claude Code: a transcript, a memory file, and the index that is not one.
	w.write(t, filepath.Join(project, fixtureSessionID+".jsonl"), fmt.Sprintf(`
{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":%q,"message":{"content":"how many adapters are there"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:02Z","message":{"model":"fixture-claude-model","usage":{"input_tokens":10,"cache_creation_input_tokens":5,"cache_read_input_tokens":20,"output_tokens":7},"content":[{"type":"thinking","thinking":"there are nine"},{"type":"text","text":"nine"},{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"adapter"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]}}
{"type":"user","timestamp":"2026-08-01T10:00:20Z","message":{"content":[{"type":"text","text":"and none is lost"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:21Z","message":{"content":[{"type":"text","text":"none"}]}}
`, w.demoCwd()))
	w.write(t, filepath.Join(project, "memory", "note.md"),
		"---\nname: the-matrix\ntype: project\n---\nThe ingest matrix has nine sources.\n")
	w.write(t, filepath.Join(project, "memory", "MEMORY.md"), "- [note](note.md)\n")

	// A subagent under the layout the runtime uses today.
	w.write(t, filepath.Join(project, fixtureSessionID, "subagents", "child-1.jsonl"), `
{"type":"user","sessionId":"`+fixtureSessionID+`","agentId":"child-1","timestamp":"2026-08-01T10:00:05Z","message":{"content":[{"type":"text","text":"find the parsers"}]}}
{"type":"assistant","sessionId":"`+fixtureSessionID+`","agentId":"child-1","timestamp":"2026-08-01T10:00:06Z","message":{"model":"fixture-subagent-model","usage":{"input_tokens":4,"output_tokens":2},"content":[{"type":"text","text":"they are in parsers/"}]}}
`)

	// Instruction files are present to prove they do not become ingest content:
	// the global CLAUDE.md and the repository AGENTS.md files are configuration,
	// not memory (the sources ruling).
	w.write(t, filepath.Join(w.home, ".claude", "CLAUDE.md"), "# Global\n\nAlways TDD.\n")
	w.write(t, filepath.Join(roots.CodexRoot, "AGENTS.md"), "# Codex\n\nTest first.\n")
	w.write(t, filepath.Join(w.demoCwd(), "AGENTS.md"), "# demo\n\nRead the specification first.\n")

	// Codex: a rollout, a memory, a rule and a refused instruction document.
	w.write(t, filepath.Join(roots.CodexSessions, "2026", "08", "01", "rollout-abc.jsonl"),
		fmt.Sprintf(`
{"type":"session_meta","timestamp":"2026-08-01T09:00:00Z","payload":{"id":"codex-thread-1","cwd":%q,"timestamp":"2026-08-01T09:00:00Z","cli_version":"9.9.9","model_provider":"fixture-provider"}}
{"type":"turn_context","timestamp":"2026-08-01T09:00:01Z","payload":{"model":"fixture-codex-model","effort":"high","summary":"auto"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:01Z","payload":{"type":"user_message","message":"start the sixth fixture"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"agent_reasoning","text":"the matrix first"}}
{"type":"response_item","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"reasoning","summary":[{"text":"the matrix first"}]}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:03Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":31,"output_tokens":9,"reasoning_output_tokens":4}}}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:30Z","payload":{"type":"task_complete","last_agent_message":"running"}}
`, w.demoCwd()))
	w.write(t, filepath.Join(roots.CodexRoot, "memories", "one.md"), "Remember the idempotency contract.\n")
	w.write(t, filepath.Join(roots.CodexRoot, "rules", "custom.rules"), "do not rewrite normalized data\n")
	w.write(t, filepath.Join(roots.CodexRoot, "rules", "default.rules"), "this one is not ingested\n")
	w.write(t, filepath.Join(roots.CodexRoot, "skills", "measure", "SKILL.md"), "Measure before judging.\n")

	// Claude Desktop and Cowork, each with the layout of its own runtime.
	w.write(t, filepath.Join(roots.ClaudeDesktopSessions, "session.json"), fmt.Sprintf(`{
  "cliSessionId": "%s",
  "sessionId": "local-1",
  "cwd": %q,
  "title": "the sixth fixture",
  "createdAt": 1785542400000,
  "lastActivityAt": 1785542520000
}`, fixtureSessionID, w.demoCwd()))
	w.write(t, filepath.Join(roots.CoworkSessions, "cw.json"), fmt.Sprintf(`{
  "cliSessionId": "cowork-1",
  "cwd": %q,
  "title": "review"
}`, w.demoCwd()))
	w.write(t, filepath.Join(roots.CoworkSessions, "cw", "audit.jsonl"), `
{"type":"user","session_id":"cowork-1","_audit_timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"review the matrix"}]}}
{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:04Z","message":{"model":"fixture-cowork-model","usage":{"input_tokens":6,"output_tokens":1},"content":"reviewed"}}
`)

	// Pi, with its tree.
	w.write(t, filepath.Join(roots.PiSessions, w.projectDir(), "session.jsonl"),
		fmt.Sprintf(`{"type":"session","version":3,"id":"pi-1","cwd":%q,"timestamp":"2026-08-01T13:00:00Z"}
{"id":"p1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"count the sources"}}
{"id":"p2","parentId":"p1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","stopReason":"stop","model":"fixture-pi-model","provider":"fixture-pi-provider","usage":{"input":12,"output":5,"reasoning":2,"cacheRead":3,"cost":{"total":0.25}},"content":[{"type":"text","text":"nine"}]}}
`, w.demoCwd()))

	// Grok Build, which files each session under the URL-escaped working
	// directory it ran in, with the update stream and its metadata side by side.
	w.seedGrok(t, roots)

	w.seedOpenCode(t, roots.OpenCodeDB)
	w.seedHermes(t, roots.HermesDB)
}

func (w *world) seedGrok(t *testing.T, roots Roots) {
	t.Helper()
	const grokSessionID = "22222222-3333-4444-5555-666666666666"
	session := filepath.Join(roots.GrokSessions, url.PathEscape(w.demoCwd()), grokSessionID)
	w.write(t, filepath.Join(session, "summary.json"), fmt.Sprintf(`{
  "info": {"id": %q, "cwd": %q},
  "session_summary": "Synthetic Grok fixture.",
  "created_at": "2026-08-01T14:00:00.123456Z",
  "updated_at": "2026-08-01T14:00:30Z",
  "last_active_at": "2026-08-01T14:00:30Z",
  "num_messages": 6,
  "num_chat_messages": 6,
  "current_model_id": "fixture-grok-model",
  "git_root_dir": %q,
  "git_remotes": ["https://synthetic.example/la-roca"],
  "head_commit": "0123456789abcdef",
  "head_branch": "main",
  "agent_name": "fixture-grok-agent",
  "sandbox_profile": "off",
  "reasoning_effort": "high",
  "request_id": "req-fixture-grok",
  "chat_format_version": 1,
  "generated_title": "the ninth fixture"
}`, grokSessionID, w.demoCwd(), w.demoCwd()))
	w.write(t, filepath.Join(session, "updates.jsonl"), `
{"method":"_x.ai/session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","update":{"sessionUpdate":"hook_execution","event_name":"synthetic_hook"}},"timestamp":1785592800}
{"method":"_x.ai/session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","update":{"sessionUpdate":"turn_completed"}},"timestamp":1785592800}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"how many sources does the fixture have"},"_meta":{"modelId":"fixture-grok-model","promptIndex":1}}},"timestamp":1785592801}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"the matrix has nine sources"}}},"timestamp":1785592802}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"let me count them"}}},"timestamp":1785592803}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"tool_call","toolCallId":"call-grok-1","title":"Read fixture","rawInput":{"target_file":"/synthetic/demo/plan.md"},"_meta":{"x.ai/tool":{"name":"read_file"}}}},"timestamp":1785592804}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"tool_call_update","toolCallId":"call-grok-1","status":"completed","rawOutput":{"content":"synthetic fixture plan"}}},"timestamp":1785592805}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"nine sources verified"}}},"timestamp":1785592806}
{"method":"session/update","params":{"sessionId":"22222222-3333-4444-5555-666666666666","_meta":{"promptId":"fixture-prompt"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"nine sources, and none is lost"}}},"timestamp":1785592807}
`)
}

func (w *world) seedOpenCode(t *testing.T, path string) {
	t.Helper()
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT)`)
	exec(t, db, `CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
	              directory TEXT, version TEXT, time_created INTEGER, time_updated INTEGER, agent TEXT)`)
	exec(t, db, `CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT,
	              time_created INTEGER, time_updated INTEGER, data TEXT)`)
	exec(t, db, `CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
	              time_created INTEGER, time_updated INTEGER, data TEXT)`)
	exec(t, db, `INSERT INTO project VALUES ('pr1', ?)`, w.demoCwd())
	exec(t, db, `INSERT INTO session VALUES ('oc1','pr1',NULL,?, '1.0', 1785542400000, 1785542520000, 'build')`,
		w.demoCwd())
	exec(t, db, `INSERT INTO message VALUES ('m1','oc1',1785542400000,1785542400000,
	              '{"role":"user","time":{"created":1785542400000}}')`)
	exec(t, db, `INSERT INTO message VALUES ('m2','oc1',1785542401000,1785542402000,
	              '{"role":"assistant","parentID":"m1","time":{"created":1785542401000,"completed":1785542402000},
	                "modelID":"fixture-opencode-model","providerID":"fixture-opencode-provider","cost":0.5,
	                "tokens":{"input":40,"output":11,"reasoning":6,"cache":{"read":2,"write":1}}}')`)
	exec(t, db, `INSERT INTO part VALUES ('p1','m1','oc1',1785542400000,1785542400000,
	              '{"type":"text","text":"what remains in the fixture"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p2','m2','oc1',1785542401500,1785542402000,
	              '{"type":"text","text":"the verification"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p3','m2','oc1',1785542401600,1785542402000,
	              '{"type":"reasoning","text":"measurement remains"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p4','m2','oc1',1785542401700,1785542402000,
	              '{"type":"tool","tool":"bash","state":{"status":"completed"}}')`)
}

func (w *world) seedHermes(t *testing.T, path string) {
	t.Helper()
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT,
	              title TEXT, started_at REAL, ended_at REAL, end_reason TEXT,
	              message_count INTEGER, tool_call_count INTEGER, input_tokens INTEGER,
	              output_tokens INTEGER, reasoning_tokens INTEGER, actual_cost_usd REAL,
	              billing_provider TEXT)`)
	exec(t, db, `CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT,
	              content TEXT, reasoning_content TEXT, tool_calls TEXT, tool_name TEXT,
	              timestamp REAL, active INTEGER, finish_reason TEXT)`)
	exec(t, db, `INSERT INTO sessions VALUES ('h1','tui','test-model',?,'a session',
	              1785542400, 1785542700, 'stop', 4, 1, 120, 33, 8, 0.75, 'fixture-hermes-provider')`,
		w.demoCwd())
	exec(t, db, `INSERT INTO messages VALUES (1,'h1','user','how many sources does it ingest',NULL,NULL,NULL,1785542400,1,NULL)`)
	exec(t, db, `INSERT INTO messages VALUES (2,'h1','assistant','nine','we have to count',
	              '[{"function":{"name":"grep","arguments":"{\"pattern\":\"source\"}"}}]',NULL,1785542401,1,'stop')`)
	exec(t, db, `INSERT INTO messages VALUES (3,'h1','tool','{"ok":true}',NULL,NULL,'grep',1785542402,1,NULL)`)
	// A session Hermes has not closed: it is read whole, and its unanswered
	// human turn is deferred rather than stored as an answer.
	exec(t, db, `INSERT INTO sessions VALUES ('h2','tui','test-model',?,'live',
	              1785542800, NULL, NULL, 1, 0, 10, 1, 0, NULL, 'fixture-hermes-provider')`,
		w.demoCwd())
	exec(t, db, `INSERT INTO messages VALUES (4,'h2','user','in progress',NULL,NULL,NULL,1785542800,1,NULL)`)
}

func openSynthetic(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open the synthetic database %s: %v", path, err)
	}
	return db
}

func exec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// rocaDatabase is a real La Roca database with the v1 schema in it.
func rocaDatabase(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.ApplySchema(context.Background(), db); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func registry(t *testing.T) layers.Registry {
	t.Helper()
	loaded, err := layers.Load()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	return loaded
}

// runIngestOn runs one normal ingest over an already-open database.
func runIngestOn(t *testing.T, db *store.DB, roots Roots) Result {
	t.Helper()
	result, err := Run(context.Background(), db, registry(t), Options{Roots: roots})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// runIngest opens a fresh database and runs one normal ingest over roots,
// returning the database for post-run assertions.
func runIngest(t *testing.T, roots Roots) (*store.DB, Result) {
	t.Helper()
	db := rocaDatabase(t)
	return db, runIngestOn(t, db, roots)
}

// assertMemoryProject asserts the memory's project attribution.
func assertMemoryProject(t *testing.T, db *sql.DB, path, want string) {
	t.Helper()
	var project string
	if err := db.QueryRow(`SELECT COALESCE(project, '') FROM memories
		WHERE json_extract(metadata, '$.file_path') = ?`, path).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if project != want {
		t.Errorf("project = %q, want %q", project, want)
	}
}

// assertClaudeMemoryInserted asserts a normal ingest landed the memory and
// attributed it to the given project.
func assertClaudeMemoryInserted(t *testing.T, result Result, db *store.DB, memory, project string) {
	t.Helper()
	if result.Sources["claude"].MemoriesInserted == 0 {
		t.Fatalf("Claude counts = %+v", result.Sources["claude"])
	}
	assertMemoryProject(t, db.SQL(), memory, project)
}
