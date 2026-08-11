//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/cucumber/godog"
)

// The operator's world: the artefact families the agents leave on disk,
// every one of them invented here. The suite is black box, so it writes the tree
// with its own hands and never imports a symbol of the product to do it.

// seededSources are the counters `roca ingest` has to report a number for. The
// scenario says "a count for every seeded source", and this is the list of what
// this world seeds.
var seededSources = []string{
	"claude_memory_files", "codex_files", "session_files",
	"codex_session_files", "claude_desktop_files", "cowork_files",
	"subagent_files", "pi_session_files", "opencode_databases", "hermes_databases",
}

func registerIngestSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Then(`^the JSON output reports a count for every seeded source$`, m.aCountForEverySeededSource)
	ctx.Then(`^the delta of the second ingest is zero in every category$`, m.theDeltaIsZero)
	ctx.Then(`^the output is valid JSON$`, m.theOutputIsValidJSON)
	ctx.Then(`^the database has not changed$`, m.theDatabaseHasNotChanged)
	ctx.Then(`^the output does not contain "([^"]*)"$`, m.theOutputDoesNotContain)
}

// --- the seeded world ---

const seededSessionID = "11111111-2222-3333-4444-555555555555"

// operatorWorld writes every source artefact under this scenario's
// HOME and takes a fingerprint of the database, so a step can later prove that a
// dry run touched nothing.
func (m *world) theOperatorsArtefacts() error {
	home := m.home
	workspace := filepath.Join(home, "w")
	demo := filepath.Join(workspace, "demo")
	// The project directory name is the working directory with its separators
	// replaced by dashes, which is how the agents encode it.
	projectDir := "-" + strings.ReplaceAll(strings.TrimPrefix(demo, "/"), "/", "-")
	claudeProjects := filepath.Join(home, ".claude", "projects", projectDir)
	codex := filepath.Join(home, ".codex")
	appSupport := m.appSupport()

	files := map[string]string{
		filepath.Join(claudeProjects, seededSessionID+".jsonl"): fmt.Sprintf(`
{"type":"user","timestamp":"2026-08-01T10:00:00Z","cwd":%q,"message":{"content":"how many source families are ingested"}}
{"type":"assistant","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"thinking","thinking":"there are nine families"},{"type":"text","text":"nine"},{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"source"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":false}]}}
{"type":"user","timestamp":"2026-08-01T10:00:30Z","message":{"content":[{"type":"text","text":"and none are lost"}]}}
{"type":"assistant","timestamp":"2026-08-01T10:00:31Z","message":{"content":[{"type":"text","text":"none"}]}}
`, demo),
		filepath.Join(claudeProjects, "memory", "matrix.md"): "---\nname: source-matrix\ntype: project\n---\n" +
			"The v1 ingest matrix has nine distinct source families, " +
			"and each adapter follows its own path without touching the others.\n",
		filepath.Join(claudeProjects, "memory", "MEMORY.md"): "- [source matrix](matrix.md)\n",
		filepath.Join(claudeProjects, seededSessionID, "subagents", "child-1.jsonl"): `
{"type":"user","sessionId":"` + seededSessionID + `","agentId":"child-1","timestamp":"2026-08-01T10:00:10Z","message":{"content":[{"type":"text","text":"find the adapters"}]}}
{"type":"assistant","sessionId":"` + seededSessionID + `","agentId":"child-1","timestamp":"2026-08-01T10:00:11Z","message":{"content":[{"type":"text","text":"they are in internal/ingest"}]}}
`,
		filepath.Join(home, ".claude", "CLAUDE.md"): "# Global\n\nRun the synthetic test first.\n",
		filepath.Join(codex, "AGENTS.md"):           "# Codex\n\nRead the complete synthetic specification first.\n",
		filepath.Join(demo, "AGENTS.md"):            "# demo\n\nThis project declares its executable contract.\n",
		filepath.Join(codex, "sessions", "2026", "08", "01", "rollout-abc.jsonl"): fmt.Sprintf(`
{"type":"session_meta","timestamp":"2026-08-01T09:00:00Z","payload":{"id":"codex-thread-1","cwd":%q,"timestamp":"2026-08-01T09:00:00Z","cli_version":"9.9.9"}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:01Z","payload":{"type":"user_message","message":"start the ingest fixture"}}
{"type":"response_item","timestamp":"2026-08-01T09:00:02Z","payload":{"type":"reasoning","summary":[{"text":"the matrix first"}]}}
{"type":"event_msg","timestamp":"2026-08-01T09:00:40Z","payload":{"type":"task_complete","last_agent_message":"running"}}
`, demo),
		filepath.Join(codex, "memories", "contract.md"):       "One idempotency contract.\n",
		filepath.Join(codex, "rules", "custom.rules"):         "do not rewrite normalized data\n",
		filepath.Join(codex, "rules", "default.rules"):        "this is not ingested\n",
		filepath.Join(codex, "skills", "measure", "SKILL.md"): "Measure before judging.\n",
		filepath.Join(appSupport, "claude-code-sessions", "session.json"): fmt.Sprintf(`{
  "cliSessionId": "%s",
  "sessionId": "local-1",
  "cwd": %q,
  "title": "the ingest fixture",
  "createdAt": 1785542400000,
  "lastActivityAt": 1785542520000
}`, seededSessionID, demo),
		filepath.Join(appSupport, "local-agent-mode-sessions", "cw.json"): fmt.Sprintf(`{
  "cliSessionId": "cowork-1",
  "cwd": %q,
  "title": "matrix review"
}`, demo),
		filepath.Join(appSupport, "local-agent-mode-sessions", "cw", "audit.jsonl"): `
{"type":"user","session_id":"cowork-1","_audit_timestamp":"2026-08-01T12:00:00Z","message":{"content":[{"type":"text","text":"review the matrix"}]}}
{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:04Z","message":{"content":"reviewed"}}
`,
		filepath.Join(home, ".pi", "agent", "sessions", projectDir, "session.jsonl"): fmt.Sprintf(
			`{"type":"session","version":3,"id":"pi-1","cwd":%q,"timestamp":"2026-08-01T13:00:00Z"}
{"id":"p1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"count the sources"}}
{"id":"p2","parentId":"p1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"nine"}]}}
`, demo),
	}
	for path, content := range files {
		if err := writeFixture(path, content); err != nil {
			return err
		}
	}
	if err := m.seedOpenCode(demo); err != nil {
		return err
	}
	if err := m.seedHermes(demo); err != nil {
		return err
	}

	// The workspace root is declared, because without it the project instruction
	// files are not looked for at all.
	return m.writeConfig("[defaults]\nworkspace_roots = [\"" + workspace + "\"]\n")
}

// operatorWorld is that same corpus with the database's fingerprint taken, for
// the scenarios that start from an installation that already exists and then
// ask whether a command wrote to it.
//
// The two are split because the journey the magic minute measures starts the
// other way round: the artefacts are on disk BEFORE there is any database, and
// `roca init` is what finds them.
func (m *world) operatorWorld() error {
	if err := m.theOperatorsArtefacts(); err != nil {
		return err
	}
	fingerprint, err := m.databaseFingerprint()
	if err != nil {
		return err
	}
	m.dbFingerprint = fingerprint
	return nil
}

// appSupport is where the desktop runtimes keep their sessions on the platform
// running the suite.
func (m *world) appSupport() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(m.home, "Library", "Application Support", "Claude")
	case "windows":
		return filepath.Join(m.home, "AppData", "Roaming", "Claude")
	default:
		return filepath.Join(m.home, ".config", "Claude")
	}
}

func (m *world) seedOpenCode(demo string) error {
	path := filepath.Join(m.home, ".local", "share", "opencode", "opencode.db")
	db, err := m.openSQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	statements := []struct {
		sql  string
		args []any
	}{
		{`CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT)`, nil},
		{`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT,
		   directory TEXT, version TEXT, time_created INTEGER, time_updated INTEGER, agent TEXT)`, nil},
		{`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT,
		   time_created INTEGER, time_updated INTEGER, data TEXT)`, nil},
		{`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
		   time_created INTEGER, time_updated INTEGER, data TEXT)`, nil},
		{`INSERT INTO project VALUES ('pr1', ?)`, []any{demo}},
		{`INSERT INTO session VALUES ('oc1','pr1',NULL,?,'1.0',1785542400000,1785542520000,'build')`,
			[]any{demo}},
		{`INSERT INTO message VALUES ('m1','oc1',1785542400000,1785542400000,
		   '{"role":"user","time":{"created":1785542400000}}')`, nil},
		{`INSERT INTO message VALUES ('m2','oc1',1785542401000,1785542402000,
		   '{"role":"assistant","parentID":"m1","time":{"created":1785542401000,"completed":1785542402000}}')`, nil},
		{`INSERT INTO part VALUES ('p1','m1','oc1',1785542400000,1785542400000,
		   '{"type":"text","text":"what remains in the fixture"}')`, nil},
		{`INSERT INTO part VALUES ('p2','m2','oc1',1785542401500,1785542402000,
		   '{"type":"text","text":"verification against reality"}')`, nil},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.sql, statement.args...); err != nil {
			return fmt.Errorf("seed OpenCode: %w", err)
		}
	}
	return nil
}

func (m *world) seedHermes(demo string) error {
	path := filepath.Join(m.home, ".hermes", "state.db")
	db, err := m.openSQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	statements := []struct {
		sql  string
		args []any
	}{
		{`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT,
		   title TEXT, started_at REAL, ended_at REAL, end_reason TEXT, message_count INTEGER)`, nil},
		{`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT,
		   reasoning_content TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL,
		   active INTEGER, finish_reason TEXT)`, nil},
		{`INSERT INTO sessions VALUES ('h1','tui','synthetic-model',?,'a synthetic session',
		   1785542400,1785542700,'stop',3)`, []any{demo}},
		{`INSERT INTO messages VALUES (1,'h1','user','how many sources are ingested',NULL,NULL,NULL,1785542400,1,NULL)`, nil},
		{`INSERT INTO messages VALUES (2,'h1','assistant','nine','they must be counted',NULL,NULL,1785542401,1,'stop')`, nil},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.sql, statement.args...); err != nil {
			return fmt.Errorf("seed Hermes: %w", err)
		}
	}
	return nil
}

func (m *world) openSQLite(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path)
}

func writeFixture(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// writeConfig writes the operator's configuration where an installation keeps it,
// next to the database it belongs to.
func (m *world) writeConfig(content string) error {
	return writeFixture(filepath.Join(m.home, ".roca", "config.toml"), content)
}

// --- the assertions ---

func (m *world) aCountForEverySeededSource() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	scanned, ok := document["scanned"].(map[string]any)
	if !ok {
		return fmt.Errorf("the output reports no scan: %v", keys(document))
	}
	for _, source := range seededSources {
		count, present := scanned[source]
		if !present {
			return fmt.Errorf("the source %q is not counted: %v", source, keys(scanned))
		}
		if number, ok := count.(float64); !ok || number < 1 {
			return fmt.Errorf("the source %q counts %v, and this world seeded it", source, count)
		}
	}
	return nil
}

func (m *world) theDeltaIsZero() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	delta, ok := document["delta"].(map[string]any)
	if !ok {
		return fmt.Errorf("the output reports no delta: %v", keys(document))
	}
	if len(delta) == 0 {
		return fmt.Errorf("the delta is empty, and an empty delta proves nothing")
	}
	for category, value := range delta {
		if number, ok := value.(float64); !ok || number != 0 {
			return fmt.Errorf("delta[%s] = %v, want 0", category, value)
		}
	}
	return nil
}

func (m *world) theOutputIsValidJSON() error {
	_, err := m.json()
	return err
}

func (m *world) theOutputDoesNotContain(text string) error {
	all := m.last.stdout + m.last.stderr
	if strings.Contains(all, text) {
		return fmt.Errorf("the output contains %q:\n%s", text, all)
	}
	return nil
}

// theDatabaseHasNotChanged compares the whole content of the tables the ingest
// writes with the fingerprint taken when the world was seeded.
func (m *world) theDatabaseHasNotChanged() error {
	fingerprint, err := m.databaseFingerprint()
	if err != nil {
		return err
	}
	if fingerprint != m.dbFingerprint {
		return fmt.Errorf("the database changed, and this command writes nothing")
	}
	return nil
}

// databaseFingerprint hashes every row of every table the ingest writes, plus the
// ingest state itself. Counting rows would miss a row swapped for another.
func (m *world) databaseFingerprint() (string, error) {
	db, err := m.openDB()
	if err != nil {
		return "", err
	}
	defer db.Close()

	hash := sha256.New()
	for _, statement := range []string{
		`SELECT session_id, COALESCE(source_agent,''), COALESCE(project,''), COALESCE(metadata,'')
		 FROM sessions ORDER BY session_id`,
		`SELECT session_id, COALESCE(exchange_number,-1), COALESCE(human_text,''),
		        COALESCE(agent_text,'') FROM exchanges ORDER BY session_id, exchange_number`,
		`SELECT session_id, COALESCE(exchange_number,-1), full_text FROM thinking_blocks
		 ORDER BY session_id, exchange_number, full_text`,
		`SELECT session_id, COALESCE(exchange_number,-1), tool_name FROM tool_uses
		 ORDER BY session_id, exchange_number, tool_name`,
		`SELECT layer, content, COALESCE(metadata,'') FROM memories ORDER BY content`,
		`SELECT path, COALESCE(fingerprint,''), COALESCE(last_error,'') FROM ingest_file_state
		 ORDER BY path`,
	} {
		rows, err := db.Query(statement)
		if err != nil {
			return "", fmt.Errorf("fingerprint the database: %w", err)
		}
		columns, _ := rows.Columns()
		for rows.Next() {
			cells := make([]any, len(columns))
			for i := range cells {
				cells[i] = new(any)
			}
			if err := rows.Scan(cells...); err != nil {
				rows.Close()
				return "", err
			}
			for _, cell := range cells {
				fmt.Fprintf(hash, "%v\x00", *(cell.(*any)))
			}
			fmt.Fprint(hash, "\x01")
		}
		rows.Close()
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
