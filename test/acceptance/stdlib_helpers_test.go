//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every parser and config reader whose hand-written helper became cmp.Or or
// slices.Sorted(maps.Keys(...)) runs here under the real binary: one home holds
// a file of each family, one `roca ingest` reads them, and the corpus says what
// each one became.
func TestStdlibHelpersRunUnderTheRealBinary(t *testing.T) {
	m := aWorldIn(t, "stdlib-helpers")
	conformance := filepath.Join("..", "..", "pkg", "parsers", "testdata", "conformance")
	copyFixture := func(source, target string) {
		t.Helper()
		if err := writeFixture(filepath.Join(m.home, target), string(mustRead(filepath.Join(conformance, source)))); err != nil {
			t.Fatal(err)
		}
	}
	write := func(target, body string) {
		t.Helper()
		if err := writeFixture(filepath.Join(m.home, target), body); err != nil {
			t.Fatal(err)
		}
	}

	copyFixture("glm-skill/skill.data", ".glm/skills/synthetic-compass/SKILL.md")
	copyFixture("grok-memory/MEMORY.golden", ".grok/memory-v2/workspaces/cobalt-harbor/MEMORY.md")
	copyFixture("hermes-memory/MEMORY.md.golden", ".hermes/memories/MEMORY.md")
	copyFixture("cursor-store/store.db", ".cursor/chats/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/11111111-aaaa-4bbb-8ccc-222222222222/store.db")
	// The sidecar names no update instant, so the session ends when its last exchange does.
	write(".cursor/chats/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/11111111-aaaa-4bbb-8ccc-222222222222/meta.json",
		`{"schemaVersion":1,"createdAtMs":1785589400000,"hasConversation":true,"title":"Synthetic harbor session","cwd":"/synthetic/harbor"}`)
	copyFixture("cursor-database/state.vscdb", "Library/Application Support/Cursor/User/globalStorage/state.vscdb")

	// Qwen Code: a tool call that fails, and a record whose type nobody named.
	write(".qwen/projects/-synthetic-orbit/chats/fixture-qwen.jsonl",
		string(mustRead(filepath.Join(conformance, "qwen-code-session/session.data")))+"\n"+
			`{"type":"user","sessionId":"fixture-qwen","timestamp":"2026-08-01T09:01:00.000Z","gitBranch":"main","message":{"role":"user","parts":[{"text":"Probe the invented crater."}]}}`+"\n"+
			`{"type":"assistant","sessionId":"fixture-qwen","timestamp":"2026-08-01T09:01:01.000Z","message":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"probe","args":{"target":"crater"}}}]}}`+"\n"+
			`{"type":"tool_result","sessionId":"fixture-qwen","timestamp":"2026-08-01T09:01:02.000Z","toolCallResult":{"status":"error"},"message":{"parts":[{"functionResponse":{"id":"call-1","name":"probe","response":{"output":"no"}}}]}}`+"\n"+
			`{"type":"assistant","sessionId":"fixture-qwen","timestamp":"2026-08-01T09:01:03.000Z","message":{"role":"model","parts":[{"text":"The crater is closed."}]}}`+"\n"+
			`{"sessionId":"fixture-qwen","timestamp":"2026-08-01T09:01:04.000Z"}`+"\n")

	// Codex: the synthetic rollout, then a prompt in the typeless legacy shape.
	write(".codex/sessions/2026/08/01/rollout-fixture-codex.jsonl",
		string(mustRead(filepath.Join(conformance, "codex-session/rollout.data")))+"\n"+
			`{"session_id":"fixture-codex","text":"A legacy prompt without a type.","ts":1785585600}`+"\n")

	// Grok: records the build does not name, attachments, a tool and a tool update
	// that arrives before any prompt.
	grok := `{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"tool_call_update"}},"timestamp":1785596399}` + "\n" +
		`{"params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{}},"timestamp":1785596399}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"user_message_chunk","content":{"type":"image","mimeType":"image/png"},"_meta":{"promptIndex":1}}},"timestamp":1785596400}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"},"_meta":{"promptIndex":1}}},"timestamp":1785596400}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"tool_call","toolCallId":"t1","title":"Read a file"}},"timestamp":1785596401}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"mystery"}},"timestamp":1785596401}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"image","mimeType":"image/png"}}},"timestamp":1785596402}` + "\n" +
		`{"method":"session/update","params":{"sessionId":"33333333-4444-5555-6666-777777777777","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}},"timestamp":1785596403}` + "\n"
	write(".grok/sessions/%2Fsynthetic%2Fgrok/33333333-4444-5555-6666-777777777777/updates.jsonl", grok)
	write(".grok/sessions/%2Fsynthetic%2Fgrok/33333333-4444-5555-6666-777777777777/summary.json",
		`{"info":{"id":"33333333-4444-5555-6666-777777777777","cwd":"/synthetic/grok"},"created_at":"2026-08-01T15:00:00Z","updated_at":"2026-08-01T15:00:01Z"}`)

	// Config: a remote source with a key nobody knows, beside a section nobody knows.
	remote := filepath.Join(m.home, "remote-home")
	if err := os.MkdirAll(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := m.writeConfig("[sources]\nunknown = 1\n\n[[sources.remote]]\nmachine = \"far\"\nroot = " +
		"\"" + remote + "\"\nextra = 1\n"); err != nil {
		t.Fatal(err)
	}

	if output, code := m.runUnder(t, nil, "init", "--db-path", filepath.Join(m.home, ".roca", "roca.db"), "--json"); code != 0 {
		t.Fatalf("init: exit %d: %s", code, output)
	}
	output, code := m.runUnder(t, nil, "ingest", "--json")
	if code != 0 {
		t.Fatalf("ingest: exit %d: %s", code, output)
	}
	rows, code := m.runUnder(t, nil, "exec", "SELECT session_id FROM plugin_roca_corpus.sessions ORDER BY session_id")
	if code != 0 {
		t.Fatalf("exec: exit %d: %s", code, rows)
	}
	for _, want := range []string{"cursor:11111111-aaaa-4bbb-8ccc-222222222222", "cursor:11111111-2222-3333-4444-555555555555",
		"fixture-qwen", "fixture-codex", "33333333-4444-5555-6666-777777777777"} {
		if !strings.Contains(rows, want) {
			t.Errorf("session %q was not ingested:\n%s", want, rows)
		}
	}
	memories, code := m.runUnder(t, nil, "exec", "SELECT source_agent, count(*) AS n FROM plugin_roca_corpus.memories GROUP BY source_agent ORDER BY source_agent")
	if code != 0 {
		t.Fatalf("exec: exit %d: %s", code, memories)
	}
	for _, want := range []string{"glm", "grok", "hermes"} {
		if !strings.Contains(memories, want) {
			t.Errorf("no memory from %q:\n%s", want, memories)
		}
	}

	// A repair nobody wrote is refused by naming the ones that exist, in order.
	refusal, code := m.runUnder(t, nil, "doctor", "repair", "nonexistent_check")
	if code == 0 || !strings.Contains(refusal, `unknown health repair "nonexistent_check"; known: orphan_supersedes, physical_alias_layer_rows, runtime_layers_not_in_registry, test_metadata_rows, test_source_agent_rows`) {
		t.Fatalf("doctor repair: exit %d: %s", code, refusal)
	}
}
