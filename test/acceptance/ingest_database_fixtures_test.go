//go:build acceptance

package acceptance

import (
	"database/sql"
	"fmt"
	"path/filepath"
)

func (w *ingestAcceptanceWorld) seedOpenCodeSession() error {
	path := filepath.Join(w.home, ".local", "share", "opencode", "opencode.db")
	db, err := openFixtureDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	cwd := filepath.Join(w.home, "workspace", "opencode-project")
	statements := []struct {
		query string
		args  []any
	}{
		{`CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT)`, nil},
		{`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT, parent_id TEXT, directory TEXT, version TEXT, time_created INTEGER, time_updated INTEGER, agent TEXT)`, nil},
		{`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`, nil},
		{`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)`, nil},
		{`CREATE TABLE todo (session_id TEXT, content TEXT, status TEXT, priority TEXT,
		   position INTEGER, time_created INTEGER, time_updated INTEGER,
		   PRIMARY KEY (session_id, position))`, nil},
		{`CREATE TABLE event (id TEXT PRIMARY KEY, aggregate_id TEXT, seq INTEGER,
		   type TEXT, data TEXT)`, nil},
		{`INSERT INTO project VALUES ('project-1', ?)`, []any{cwd}},
		{`INSERT INTO session VALUES ('opencode-acceptance-session','project-1',NULL,?,'1',1785542400000,1785542460000,'build')`, []any{cwd}},
		{`INSERT INTO message VALUES ('m1','opencode-acceptance-session',1785542400000,1785542400000,'{"role":"user","time":{"created":1785542400000}}')`, nil},
		{`INSERT INTO message VALUES ('m2','opencode-acceptance-session',1785542401000,1785542402000,'{"role":"assistant","parentID":"m1","time":{"created":1785542401000,"completed":1785542402000},"modelID":"synthetic-opencode-a"}')`, nil},
		{`INSERT INTO message VALUES ('m3','opencode-acceptance-session',1785542401500,1785542402000,'{"role":"assistant","parentID":"m1","time":{"created":1785542401500,"completed":1785542402000},"modelID":"synthetic-opencode-b"}')`, nil},
		{`INSERT INTO part VALUES ('p1','m1','opencode-acceptance-session',1785542400000,1785542400000,'{"type":"text","text":"question"}')`, nil},
		{`INSERT INTO part VALUES ('p2','m2','opencode-acceptance-session',1785542401000,1785542402000,'{"type":"text","text":"answer"}')`, nil},
		{`INSERT INTO part VALUES ('p3','m2','opencode-acceptance-session',1785542401100,1785542401100,'{"type":"reasoning","text":"synthetic reasoning"}')`, nil},
		{`INSERT INTO part VALUES ('p4','m2','opencode-acceptance-session',1785542401200,1785542401200,'{"type":"tool","tool":"read","state":{"status":"completed","input":{"filePath":"synthetic.txt"}}}')`, nil},
		{`INSERT INTO part VALUES ('p5','m2','opencode-acceptance-session',1785542401300,1785542401300,'{"type":"patch","hash":"synthetic-hash","files":["synthetic.txt"]}')`, nil},
		{`INSERT INTO part VALUES ('p6','m2','opencode-acceptance-session',1785542401400,1785542401400,'{"type":"step-start","snapshot":"OPENCODE-TELEMETRY-SENTINEL"}')`, nil},
		{`INSERT INTO part VALUES ('p7','m3','opencode-acceptance-session',1785542401500,1785542402000,'{"type":"text","text":"second answer"}')`, nil},
		{`INSERT INTO todo VALUES ('opencode-acceptance-session','synthetic task','pending','high',0,1785542400000,1785542401000)`, nil},
		{`INSERT INTO event VALUES ('event-1','opencode-acceptance-session',1,'telemetry','{"payload":"OPENCODE-EVENT-SENTINEL"}')`, nil},
	}
	if err := execFixtureSQL(db, statements); err != nil {
		return err
	}
	w.sessionID, w.fixturePath = "opencode-acceptance-session", path
	return nil
}

func (w *ingestAcceptanceWorld) seedZCodeSession() error {
	path := filepath.Join(w.home, ".zcode", "cli", "db", "db.sqlite")
	db, err := openFixtureDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	cwd := filepath.Join(w.home, "workspace", "zcode-project")
	statements := []struct {
		query string
		args  []any
	}{
		{`CREATE TABLE schema_migration (id TEXT PRIMARY KEY, checksum TEXT NOT NULL,
		   app_version TEXT, time_applied INTEGER NOT NULL)`, nil},
		{`CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, workspace_id TEXT,
		   parent_id TEXT, slug TEXT NOT NULL, directory TEXT NOT NULL, path TEXT, title TEXT NOT NULL,
		   version TEXT NOT NULL, task_type TEXT NOT NULL, time_created INTEGER NOT NULL,
		   time_updated INTEGER NOT NULL)`, nil},
		{`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL,
		   time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL,
		   sequence INTEGER)`, nil},
		{`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL,
		   time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, data TEXT NOT NULL,
		   sequence INTEGER)`, nil},
		{`INSERT INTO schema_migration VALUES
		   ('0018_session_input_failed_status','synthetic','0.15.2',1785542300000)`, nil},
		{`INSERT INTO session VALUES
		   ('zcode-acceptance-session','project-1','workspace-1',NULL,'zcode-acceptance-session',?,NULL,
		    'Synthetic ZCode session','0.16.5','interactive',1785542400000,1785542460000)`, []any{cwd}},
		{`INSERT INTO message VALUES
		   ('z-user','zcode-acceptance-session',1785542400000,1785542400000,
		    '{"role":"user","time":{"created":1785542400000},"model":{"providerID":"synthetic-zcode-provider","modelID":"synthetic-zcode-model"},"semantics":{"origin":"real_user","kind":"user_prompt","uiVisibility":"visible","transcriptVisibility":"visible"}}',0)`, nil},
		{`INSERT INTO message VALUES
		   ('z-assistant','zcode-acceptance-session',1785542401000,1785542402000,
		    '{"role":"assistant","parentID":"z-user","time":{"created":1785542401000,"completed":1785542402000},"modelID":"synthetic-zcode-model","providerID":"synthetic-zcode-provider","semantics":{"origin":"agent_runtime","kind":"assistant_response","uiVisibility":"visible","transcriptVisibility":"visible"}}',1)`, nil},
		{`INSERT INTO message VALUES
		   ('z-reminder','zcode-acceptance-session',1785542401500,1785542401500,
		    '{"role":"user","time":{"created":1785542401500},"model":{"providerID":"synthetic-zcode-provider","modelID":"synthetic-zcode-model"},"synthetic":true,"semantics":{"origin":"agent_runtime","kind":"todo_reminder","uiVisibility":"hidden","transcriptVisibility":"hidden"}}',2)`, nil},
		{`INSERT INTO part VALUES
		   ('z-p1','z-user','zcode-acceptance-session',1785542400000,1785542400000,
		    '{"type":"text","text":"synthetic question"}',0)`, nil},
		{`INSERT INTO part VALUES
		   ('z-p2','z-assistant','zcode-acceptance-session',1785542401000,1785542401000,
		    '{"type":"reasoning","text":"synthetic reasoning"}',0)`, nil},
		{`INSERT INTO part VALUES
		   ('z-p3','z-assistant','zcode-acceptance-session',1785542401100,1785542401100,
		    '{"type":"text","text":"synthetic answer"}',1)`, nil},
		{`INSERT INTO part VALUES
		   ('z-p4','z-assistant','zcode-acceptance-session',1785542401200,1785542401200,
		    '{"type":"tool","tool":"Read","state":{"status":"completed","input":{"file_path":"synthetic.txt"}}}',2)`, nil},
		{`INSERT INTO part VALUES
		   ('z-p5','z-reminder','zcode-acceptance-session',1785542401500,1785542401500,
		    '{"type":"text","text":"ZCODE-HIDDEN-ACCEPTANCE-SENTINEL","synthetic":true}',0)`, nil},
	}
	if err := execFixtureSQL(db, statements); err != nil {
		return err
	}
	w.sessionID, w.fixturePath = "zcode-acceptance-session", path
	return nil
}

func (w *ingestAcceptanceWorld) seedHermesSession(model string) error {
	path := filepath.Join(w.home, ".hermes", "state.db")
	db, err := openFixtureDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	cwd := filepath.Join(w.home, "workspace", "hermes-project")
	statements := []struct {
		query string
		args  []any
	}{
		{`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT, model TEXT, cwd TEXT, title TEXT, started_at REAL, ended_at REAL, end_reason TEXT, message_count INTEGER)`, nil},
		{`CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT, reasoning_content TEXT, tool_calls TEXT, tool_name TEXT, timestamp REAL, active INTEGER, finish_reason TEXT)`, nil},
		{`INSERT INTO sessions VALUES ('hermes-acceptance-session','tui',?,?,'hermes',1785542400,1785542460,'stop',2)`, []any{model, cwd}},
		{`INSERT INTO messages VALUES (1,'hermes-acceptance-session','user','question',NULL,NULL,NULL,1785542400,1,NULL)`, nil},
		{`INSERT INTO messages VALUES (2,'hermes-acceptance-session','assistant','answer',NULL,NULL,NULL,1785542401,1,'stop')`, nil},
	}
	if err := execFixtureSQL(db, statements); err != nil {
		return err
	}
	w.sessionID, w.fixturePath = "hermes-acceptance-session", path
	return nil
}

func openFixtureDB(path string) (*sql.DB, error) {
	if err := writeFixture(path, ""); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", "file:"+path)
}

func execFixtureSQL(db *sql.DB, statements []struct {
	query string
	args  []any
}) error {
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			return fmt.Errorf("fixture SQL %q: %w", statement.query, err)
		}
	}
	return nil
}
