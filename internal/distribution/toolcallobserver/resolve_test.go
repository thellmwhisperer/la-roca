package toolcallobserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
)

const (
	claudeSessionID = "11111111-1111-1111-1111-111111111111"
	grokSessionID   = "22222222-2222-2222-2222-222222222222"
	codexThreadID   = "33333333-3333-3333-3333-333333333333"
)

func TestResolveUsesTheInvokingHarnessSessionFile(t *testing.T) {
	roots := labRoots(t)
	claudePath := filepath.Join(roots.ClaudeProjects, "synthetic-lab", claudeSessionID+".jsonl")
	grokPath := filepath.Join(roots.GrokSessions, "%2Fsynthetic%2Flab", grokSessionID, "updates.jsonl")
	codexPath := filepath.Join(roots.CodexSessions, "2026", "08", "01", codexThreadID+".jsonl")
	piPath := filepath.Join(roots.PiSessions, "demo", "session.jsonl")
	piPathTwo := filepath.Join(roots.PiSessions, "demo-two", "session.jsonl")
	openCodePath := roots.OpenCodeDB
	cursorPath := filepath.Join(roots.Home, ".cursor", "chats", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "store.db")
	mustExist := []struct{ path, body string }{
		{claudePath, claudeShellLog},
		{grokPath, grokShellLog},
		{codexPath, `{"type":"session_meta","payload":{"id":"` + codexThreadID + `"}}`},
		{piPath, `{"type":"session","version":3,"id":"pi-session-1","cwd":"/synthetic/lab","timestamp":"2026-08-01T13:00:00Z"}` + "\n"},
		{piPathTwo, `{"type":"session","version":3,"id":"pi-session-2","cwd":"/synthetic/lab-two"}` + "\n"},
		{openCodePath, "{}"},
		{cursorPath, "{}"},
	}
	for _, file := range mustExist {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name    string
		facts   Evidence
		want    string
		missing string
	}{
		{
			name: "claude process with session id",
			facts: Evidence{
				Processes: []Process{{Command: "claude"}},
				Environment: map[string]string{
					"CLAUDECODE":             "1",
					"CLAUDE_CODE_SESSION_ID": claudeSessionID,
				},
			},
			want: claudePath,
		},
		{
			name: "grok process with session id",
			facts: Evidence{
				Processes: []Process{{Command: "grok"}},
				Environment: map[string]string{
					"GROK_AGENT":             "1",
					"GROK_SESSION_ID":        grokSessionID,
					"CLAUDECODE":             "1",
					"CLAUDE_CODE_SESSION_ID": claudeSessionID,
				},
			},
			want: grokPath,
		},
		{
			name: "codex process with thread id",
			facts: Evidence{
				Processes:   []Process{{Command: "codex"}},
				Environment: map[string]string{"CODEX_THREAD_ID": codexThreadID},
			},
			want: codexPath,
		},
		{
			name: "nearest grok wins over an outer claude ancestor",
			facts: Evidence{
				Processes: []Process{
					{Command: "grok"},
					{Command: "claude"},
				},
				Environment: map[string]string{
					"GROK_SESSION_ID":        grokSessionID,
					"CLAUDE_CODE_SESSION_ID": claudeSessionID,
				},
			},
			want: grokPath,
		},
		{
			name:    "no agent in the process tree",
			facts:   Evidence{Processes: []Process{{Command: "zsh"}}},
			missing: "invoking agent",
		},
		{
			name: "claude process without a session id",
			facts: Evidence{
				Processes:   []Process{{Command: "claude"}},
				Environment: map[string]string{"CLAUDECODE": "1"},
			},
			missing: "session",
		},
		{
			name: "session id whose transcript is not on disk",
			facts: Evidence{
				Processes: []Process{{Command: "claude"}},
				Environment: map[string]string{
					"CLAUDE_CODE_SESSION_ID": "99999999-9999-9999-9999-999999999999",
				},
			},
			missing: "transcript",
		},
		{
			name: "grok process missing its own session id does not steal Claude's",
			facts: Evidence{
				Processes: []Process{{Command: "grok"}},
				Environment: map[string]string{
					"CLAUDECODE":             "1",
					"CLAUDE_CODE_SESSION_ID": claudeSessionID,
				},
			},
			missing: "Grok",
		},
		{
			name: "pi session id resolves the session file",
			facts: Evidence{
				Processes:   []Process{{Command: "pi"}},
				Environment: map[string]string{"PI_SESSION_ID": "pi-session-1"},
			},
			want: piPath,
		},
		{
			name: "pi missing session id refuses",
			facts: Evidence{
				Processes: []Process{{Command: "pi"}},
			},
			missing: "session identity",
		},
		{
			name: "pi session file is open",
			facts: Evidence{
				Processes: []Process{{Command: "pi", OpenFiles: []string{piPath}}},
			},
			want: piPath,
		},
		{
			name: "opencode database is open",
			facts: Evidence{
				Processes: []Process{{Command: "opencode", OpenFiles: []string{openCodePath}}},
			},
			want: openCodePath,
		},
		{
			name: "cursor store is open",
			facts: Evidence{
				Processes: []Process{{Command: "cursor", OpenFiles: []string{cursorPath}}},
			},
			want: cursorPath,
		},
		{
			name: "pi process with several open session files",
			facts: Evidence{
				Processes: []Process{{Command: "pi", OpenFiles: []string{piPath, piPathTwo}}},
			},
			missing: "more than one",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.facts.Roots = roots
			got, err := Resolve(test.facts)
			if test.missing != "" {
				if err == nil {
					t.Fatalf("resolved %s, want a refusal", got.Path)
				}
				if !strings.Contains(err.Error(), test.missing) {
					t.Fatalf("error %q does not name %q", err, test.missing)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Path != test.want {
				t.Fatalf("path = %q, want %q", got.Path, test.want)
			}
		})
	}
}

func labRoots(t *testing.T) ingest.Roots {
	t.Helper()
	home := t.TempDir()
	return ingest.ResolveRoots(ingest.Environment{Home: home, GOOS: "darwin"}, ingest.Settings{})
}

const claudeShellLog = `{"type":"assistant","timestamp":"2026-08-01T10:00:01Z","message":{"content":[{"type":"tool_use","id":"c1","name":"Bash","input":{"command":"echo lab"}}]}}
{"type":"user","timestamp":"2026-08-01T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"c1","content":"lab\n"}]}}
`

const grokShellLog = `{"method":"session/update","params":{"sessionId":"22222222-2222-2222-2222-222222222222","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"run it"}}},"timestamp":1785585601}
{"method":"session/update","params":{"sessionId":"22222222-2222-2222-2222-222222222222","update":{"sessionUpdate":"tool_call","toolCallId":"t1","rawInput":{"command":"echo lab"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}}},"timestamp":1785585602}
{"method":"session/update","params":{"sessionId":"22222222-2222-2222-2222-222222222222","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1","rawOutput":"lab\n"}},"timestamp":1785585603}
`
