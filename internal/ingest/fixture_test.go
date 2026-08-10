package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/layers"
	"github.com/thellmwhisperer/la-roca/internal/store"
	_ "modernc.org/sqlite"
)

// A whole synthetic world: every source of the v1 matrix, each one
// invented here. No real transcript, no real database and no private vocabulary
// ever enters a test (PRD C5).

// world is a sandbox HOME with the agents' artefacts in it.
type world struct {
	home      string
	workspace string
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
		env:       Environment{GOOS: "darwin", Home: home},
		settings:  Settings{WorkspaceRoots: []string{workspace}},
	}
	w.seed(t)
	return w
}

func (w *world) roots() Roots { return ResolveRoots(w.env, w.settings) }

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

	// Claude Code: a transcript, a memory file, and the index that is not one.
	w.write(t, filepath.Join(project, fixtureSessionID+".jsonl"), fmt.Sprintf(`
{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":%q,"message":{"content":"cuantos adaptadores hay"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"thinking","thinking":"son nueve"},{"type":"text","text":"nueve"},{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"adaptador"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]}}
{"type":"user","timestamp":"2026-08-01T10:00:20Z","message":{"content":[{"type":"text","text":"y ninguno se pierde"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:21Z","message":{"content":[{"type":"text","text":"ninguno"}]}}
`, w.demoCwd()))
	w.write(t, filepath.Join(project, "memory", "nota.md"),
		"---\nname: la-matriz\ntype: project\n---\nLa matriz de ingesta tiene nueve fuentes.\n")
	w.write(t, filepath.Join(project, "memory", "MEMORY.md"), "- [nota](nota.md)\n")

	// A subagent under the layout the runtime uses today.
	w.write(t, filepath.Join(project, fixtureSessionID, "subagents", "child-1.jsonl"), `
{"type":"user","sessionId":"`+fixtureSessionID+`","agentId":"child-1","timestamp":"2026-08-01T10:00:05Z","message":{"content":[{"type":"text","text":"busca los parsers"}]}}
{"type":"assistant","sessionId":"`+fixtureSessionID+`","agentId":"child-1","timestamp":"2026-08-01T10:00:06Z","message":{"content":[{"type":"text","text":"estan en parsers/"}]}}
`)

	// Instruction files are present to prove they do not become ingest content:
	// the global CLAUDE.md and the repository AGENTS.md files are configuration,
	// not memory (the sources ruling).
	w.write(t, filepath.Join(w.home, ".claude", "CLAUDE.md"), "# Global\n\nSiempre TDD.\n")
	w.write(t, filepath.Join(roots.CodexRoot, "AGENTS.md"), "# Codex\n\nEl test primero.\n")
	w.write(t, filepath.Join(w.demoCwd(), "AGENTS.md"), "# demo\n\nLee la spec antes.\n")

	// Codex: a rollout, a memory, a rule and a skill.
	w.write(t, filepath.Join(roots.CodexSessions, "2026", "08", "01", "rollout-abc.jsonl"),
		fmt.Sprintf(`
{"type":"session_meta","timestamp":"2026-08-01T09:00:00Z","payload":{"id":"codex-thread-1","cwd":%q,"timestamp":"2026-08-01T09:00:00Z","cli_version":"9.9.9"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:01Z","payload":{"type":"user_message","message":"arranca la ola seis"}}
{"type":"response_item","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"reasoning","summary":[{"text":"la matriz primero"}]}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:30Z","payload":{"type":"task_complete","last_agent_message":"en marcha"}}
`, w.demoCwd()))
	w.write(t, filepath.Join(roots.CodexRoot, "memories", "una.md"), "Recuerda el contrato de idempotencia.\n")
	w.write(t, filepath.Join(roots.CodexRoot, "rules", "mia.rules"), "no reescribir lo ya normalizado\n")
	w.write(t, filepath.Join(roots.CodexRoot, "rules", "default.rules"), "esta no se ingiere\n")
	w.write(t, filepath.Join(roots.CodexRoot, "skills", "medir", "SKILL.md"), "Medir antes de opinar.\n")

	// Claude Desktop and Cowork, each with the layout of its own runtime.
	w.write(t, filepath.Join(roots.ClaudeDesktopSessions, "sesion.json"), fmt.Sprintf(`{
  "cliSessionId": "%s",
  "sessionId": "local-1",
  "cwd": %q,
  "title": "la ola seis",
  "createdAt": 1785542400000,
  "lastActivityAt": 1785542520000
}`, fixtureSessionID, w.demoCwd()))
	w.write(t, filepath.Join(roots.CoworkSessions, "cw.json"), fmt.Sprintf(`{
  "cliSessionId": "cowork-1",
  "cwd": %q,
  "title": "revision"
}`, w.demoCwd()))
	w.write(t, filepath.Join(roots.CoworkSessions, "cw", "audit.jsonl"), `
{"type":"user","session_id":"cowork-1","_audit_timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"revisa la matriz"}]}}
{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:04Z","message":{"content":"revisada"}}
`)

	// Pi, with its tree.
	w.write(t, filepath.Join(roots.PiSessions, w.projectDir(), "sesion.jsonl"),
		fmt.Sprintf(`{"type":"session","version":3,"id":"pi-1","cwd":%q,"timestamp":"2026-08-01T13:00:00Z"}
{"id":"p1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"cuenta las fuentes"}}
{"id":"p2","parentId":"p1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"nueve"}]}}
`, w.demoCwd()))

	w.seedOpenCode(t, roots.OpenCodeDB)
	w.seedHermes(t, roots.HermesDB)
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
	              '{"role":"assistant","parentID":"m1","time":{"created":1785542401000,"completed":1785542402000}}')`)
	exec(t, db, `INSERT INTO part VALUES ('p1','m1','oc1',1785542400000,1785542400000,
	              '{"type":"text","text":"que falta de la ola"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p2','m2','oc1',1785542401500,1785542402000,
	              '{"type":"text","text":"la verificacion"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p3','m2','oc1',1785542401600,1785542402000,
	              '{"type":"reasoning","text":"queda medir"}')`)
	exec(t, db, `INSERT INTO part VALUES ('p4','m2','oc1',1785542401700,1785542402000,
	              '{"type":"tool","tool":"bash","state":{"status":"completed"}}')`)
}

func (w *world) seedHermes(t *testing.T, path string) {
	t.Helper()
	db := openSynthetic(t, path)
	defer db.Close()
	exec(t, db, `CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT,
	              title TEXT, started_at REAL, ended_at REAL, end_reason TEXT,
	              message_count INTEGER, tool_call_count INTEGER, input_tokens INTEGER)`)
	exec(t, db, `CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT,
	              content TEXT, reasoning_content TEXT, tool_calls TEXT, tool_name TEXT,
	              timestamp REAL, active INTEGER, finish_reason TEXT)`)
	exec(t, db, `INSERT INTO sessions VALUES ('h1','tui','modelo-de-prueba',?,'una sesion',
	              1785542400, 1785542700, 'stop', 4, 1, 120)`, w.demoCwd())
	exec(t, db, `INSERT INTO messages VALUES (1,'h1','user','cuantas fuentes ingiere',NULL,NULL,NULL,1785542400,1,NULL)`)
	exec(t, db, `INSERT INTO messages VALUES (2,'h1','assistant','nueve','hay que contar',
	              '[{"function":{"name":"grep","arguments":"{\"pattern\":\"fuente\"}"}}]',NULL,1785542401,1,'stop')`)
	exec(t, db, `INSERT INTO messages VALUES (3,'h1','tool','{"ok":true}',NULL,NULL,'grep',1785542402,1,NULL)`)
	// A session Hermes has not closed: it is not read until it has an ending.
	exec(t, db, `INSERT INTO sessions VALUES ('h2','tui','modelo-de-prueba',?,'viva',
	              1785542800, NULL, NULL, 1, 0, 10)`, w.demoCwd())
	exec(t, db, `INSERT INTO messages VALUES (4,'h2','user','en curso',NULL,NULL,NULL,1785542800,1,NULL)`)
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
