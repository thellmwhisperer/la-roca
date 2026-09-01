package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestDeclaredChatGPTCodexExportIsNeverDiscoveredWithoutADeclaration(t *testing.T) {
	roots := ResolveRoots(Environment{GOOS: "linux", Home: t.TempDir()}, Settings{})
	plan := Scan(roots)
	for _, target := range plan.Targets {
		if target.SourceAgent == "codex-cloud" || target.Kind == parsers.KindChatGPTCodex {
			t.Fatalf("undeclared cloud Codex target = %+v", target)
		}
	}
}

func TestDeclaredChatGPTExportIngestsCodexJSONUnderADistinctSource(t *testing.T) {
	db := rocaDatabase(t)
	root := filepath.Join("testdata", "openai-export-sharded")
	result, err := Run(context.Background(), db, registry(t), Options{
		Roots: Roots{ChatGPTWebExports: []string{root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned["chatgpt_web_export_files"] != 3 || result.FilesRead != 3 || result.FilesExcluded != 0 {
		t.Fatalf("sharded files scanned/read/excluded = %d/%d/%d, want 3/3/0",
			result.Scanned["chatgpt_web_export_files"], result.FilesRead, result.FilesExcluded)
	}
	if result.Sources["codex-cloud"].Sessions != 1 || result.Sources["codex-cloud"].Exchanges != 1 {
		t.Fatalf("codex-cloud counts = %+v", result.Sources["codex-cloud"])
	}
	var agent, surface, title, model, provider string
	if err := db.SQL().QueryRow(`SELECT source_agent, COALESCE(source_surface, ''), title,
		COALESCE(e.model, ''), COALESCE(e.provider, '')
		FROM sessions s JOIN exchanges e ON e.session_id = s.session_id
		WHERE s.session_id = ?`, "60000000-0000-4000-8000-000000000001").
		Scan(&agent, &surface, &title, &model, &provider); err != nil {
		t.Fatal(err)
	}
	if agent != "codex-cloud" || surface != "Codex CLI" || title != "Synthetic cloud Codex hatch" {
		t.Fatalf("session identity = %q / %q / %q", agent, surface, title)
	}
	if model != "" || provider != "" {
		t.Fatalf("invented provenance = %q/%q", model, provider)
	}
}

func TestOverlappingLocalCodexRolloutKeepsTheRicherRow(t *testing.T) {
	cloudID := "fixture-codex-overlap"
	export := t.TempDir()
	if err := os.WriteFile(filepath.Join(export, "codex.json"), []byte(`[
	  {"id":"`+cloudID+`","title":"Synthetic cloud title","archived":false,"turns":[
	    {"id":"u","role":"user","create_time":1767225601.125,"input_items":[{"type":"message","role":"user","content":[{"content_type":"text","text":"Open the imaginary hatch."}]}]},
	    {"id":"a","role":"assistant","previous_turn_id":"u","create_time":1767225602.625,"turn_status":"TaskTurnStatusEnum.COMPLETED","output_items":[
	      {"type":"message","role":"assistant","content":[{"content_type":"text","text":"The hatch is open."}]}
	    ]}
	  ]}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}
	rollout := `{"type":"session_meta","timestamp":"2026-08-01T12:00:00Z","payload":{"id":"` + cloudID + `","cwd":"/synthetic/lighthouse","timestamp":"2026-08-01T12:00:00Z","model_provider":"openai"}}
{"type":"turn_context","timestamp":"2026-08-01T12:00:01Z","payload":{"model":"gpt-synthetic-local"}}
{"type":"event_msg","timestamp":"2026-08-01T12:00:01Z","payload":{"type":"user_message","message":"Open the imaginary hatch."}}
{"type":"event_msg","timestamp":"2026-08-01T12:00:02Z","payload":{"type":"task_complete","last_agent_message":"The hatch is open."}}
`
	for _, name := range []string{"cloud then local", "local then cloud"} {
		t.Run(name, func(t *testing.T) {
			db := rocaDatabase(t)
			home := t.TempDir()
			sessions := filepath.Join(home, ".codex", "sessions", "2026", "08", "01")
			if err := os.MkdirAll(sessions, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(sessions, "rollout-overlap.jsonl"), []byte(rollout), 0o600); err != nil {
				t.Fatal(err)
			}
			cloud := Options{Roots: Roots{ChatGPTWebExports: []string{export}}}
			local := Options{Roots: Roots{
				Home: home, CodexRoot: filepath.Join(home, ".codex"),
				CodexSessions: filepath.Join(home, ".codex", "sessions"),
			}}
			order := []Options{cloud, local}
			if name == "local then cloud" {
				order[0], order[1] = local, cloud
			}
			for _, opts := range order {
				if _, err := Run(context.Background(), db, registry(t), opts); err != nil {
					t.Fatal(err)
				}
			}
			if got := countRows(t, db.SQL(), "sessions WHERE session_id = '"+cloudID+"'"); got != 1 {
				t.Fatalf("sessions = %d, want 1 merged row", got)
			}
			if got := countRows(t, db.SQL(), "exchanges WHERE session_id = '"+cloudID+"'"); got != 1 {
				t.Fatalf("exchanges = %d, want 1 merged row", got)
			}
			var title, model, provider, humanTS, agentTS string
			var latency int
			if err := db.SQL().QueryRow(`SELECT s.title, COALESCE(e.model, ''), COALESCE(e.provider, ''),
				COALESCE(e.human_timestamp, ''), COALESCE(e.agent_timestamp, ''),
				COALESCE(e.response_latency_ms, -1)
				FROM sessions s JOIN exchanges e ON e.session_id = s.session_id
				WHERE s.session_id = ?`, cloudID).
				Scan(&title, &model, &provider, &humanTS, &agentTS, &latency); err != nil {
				t.Fatal(err)
			}
			if title != "Synthetic cloud title" {
				t.Errorf("title = %q, want the cloud snapshot's title", title)
			}
			if model != "gpt-synthetic-local" || provider != "openai" {
				t.Errorf("kept provenance = %q/%q, want the richer local rollout", model, provider)
			}
			if humanTS != "2026-08-01T12:00:01Z" || agentTS != "2026-08-01T12:00:02Z" || latency != -1 {
				t.Errorf("local timing = %q / %q / %d, want rollout times and unknown latency",
					humanTS, agentTS, latency)
			}
		})
	}
}
