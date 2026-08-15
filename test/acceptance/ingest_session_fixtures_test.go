//go:build acceptance

package acceptance

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	claudeAcceptanceSession = "11111111-2222-3333-4444-555555555555"
	codexAcceptanceSession  = "codex-acceptance-session"
	grokAcceptanceSession   = "22222222-3333-4444-5555-666666666666"
)

func (w *ingestAcceptanceWorld) seedClaudeSession(project string, exchanges int, rich bool, model string) error {
	workspace := filepath.Join(w.home, "workspace")
	cwd := ""
	encoded := "-unknown-absolute-project"
	if project != "" {
		cwd = filepath.Join(workspace, project)
		encoded = encodeAgentPath(cwd)
		if err := w.writeConfig(workspace); err != nil {
			return err
		}
	}
	path := filepath.Join(w.home, ".claude", "projects", encoded, claudeAcceptanceSession+".jsonl")
	body := claudeExchange(1, cwd, rich, model)
	if exchanges > 1 {
		body += claudeExchange(2, cwd, false, model)
	}
	if err := writeFixture(path, body); err != nil {
		return err
	}
	w.fixturePath, w.sessionID = path, claudeAcceptanceSession
	return nil
}

func claudeExchange(turn int, cwd string, rich bool, model string) string {
	stamp := fmt.Sprintf("2026-08-01T10:00:%02dZ", turn*10)
	cwdField := ""
	if cwd != "" {
		cwdField = fmt.Sprintf(",\"cwd\":%q", cwd)
	}
	modelField := ""
	if model != "" {
		modelField = fmt.Sprintf(",\"model\":%q", model)
	}
	blocks := `[{"type":"text","text":"answer"}]`
	if rich {
		blocks = `[{"type":"thinking","thinking":"inspect the contract"},{"type":"text","text":"answer"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"path":"contract"}}]`
	}
	return fmt.Sprintf("{\"type\":\"user\",\"timestamp\":%q%s,\"message\":{\"content\":\"question %d\"}}\n"+
		"{\"type\":\"assistant\",\"timestamp\":%q,\"message\":{\"content\":%s%s}}\n",
		stamp, cwdField, turn, stamp, blocks, modelField)
}

func (w *ingestAcceptanceWorld) appendClaudeExchange() error {
	raw, err := os.ReadFile(w.fixturePath)
	if err != nil {
		return err
	}
	return os.WriteFile(w.fixturePath, append(raw, []byte(claudeExchange(2, "", false, ""))...), 0o600)
}

func (w *ingestAcceptanceWorld) seedCodexSession(model string) error {
	cwd := filepath.Join(w.home, "workspace", "codex-project")
	modelLine := ""
	if model != "" {
		modelLine = fmt.Sprintf("{\"type\":\"turn_context\",\"timestamp\":\"2026-08-01T11:00:01Z\",\"payload\":{\"turn_id\":\"turn-1\",\"model\":%q}}\n", model)
	}
	body := fmt.Sprintf("{\"type\":\"session_meta\",\"timestamp\":\"2026-08-01T11:00:00Z\",\"payload\":{\"id\":%q,\"cwd\":%q,\"timestamp\":\"2026-08-01T11:00:00Z\"}}\n", codexAcceptanceSession, cwd) +
		modelLine +
		"{\"type\":\"event_msg\",\"timestamp\":\"2026-08-01T11:00:02Z\",\"payload\":{\"type\":\"user_message\",\"message\":\"question\"}}\n" +
		"{\"type\":\"event_msg\",\"timestamp\":\"2026-08-01T11:00:03Z\",\"payload\":{\"type\":\"task_complete\",\"last_agent_message\":\"answer\"}}\n"
	return w.writeCodexRollout(body)
}

// writeCodexRollout files a rollout where the scanner looks for one and leaves it
// as the fixture the scenario is about.
func (w *ingestAcceptanceWorld) writeCodexRollout(body string) error {
	path := filepath.Join(w.home, ".codex", "sessions", "2026", "08", "01", "rollout.jsonl")
	if err := writeFixture(path, body); err != nil {
		return err
	}
	w.fixturePath, w.sessionID = path, codexAcceptanceSession
	return nil
}

func (w *ingestAcceptanceWorld) seedDesktopSession(model string) error {
	w.sessionID = "desktop-acceptance-session"
	body := fmt.Sprintf(`{"cliSessionId":%q,"cwd":%q,"title":"desktop","model":%q,"createdAt":1785542400000,"lastActivityAt":1785542460000}`,
		w.sessionID, filepath.Join(w.home, "workspace", "desktop-project"), model)
	path := filepath.Join(w.appSupportPath(), "claude-code-sessions", "desktop.json")
	w.fixturePath = path
	return writeFixture(path, body)
}

func (w *ingestAcceptanceWorld) seedCoworkSession() error {
	w.sessionID = "cowork-acceptance-session"
	root := filepath.Join(w.appSupportPath(), "local-agent-mode-sessions")
	metadata := filepath.Join(root, "cowork.json")
	if err := writeFixture(metadata, fmt.Sprintf(`{"cliSessionId":%q,"cwd":%q,"title":"cowork"}`,
		w.sessionID, filepath.Join(w.home, "workspace", "cowork-project"))); err != nil {
		return err
	}
	w.fixturePath = filepath.Join(root, "cowork", "audit.jsonl")
	return writeFixture(w.fixturePath,
		`{"type":"user","session_id":"cowork-acceptance-session","_audit_timestamp":"2026-08-01T12:00:00Z","message":{"content":"question"}}`+"\n"+
			`{"type":"assistant","_audit_timestamp":"2026-08-01T12:00:01Z","message":{"content":"answer"}}`+"\n")
}

func (w *ingestAcceptanceWorld) appSupportPath() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(w.home, "Library", "Application Support", "Claude")
	case "windows":
		return filepath.Join(w.home, "AppData", "Roaming", "Claude")
	default:
		return filepath.Join(w.home, ".config", "Claude")
	}
}

func (w *ingestAcceptanceWorld) seedPiSession() error {
	w.sessionID = "pi-acceptance-session"
	cwd := filepath.Join(w.home, "workspace", "pi-project")
	body := fmt.Sprintf(`{"type":"session","version":3,"id":%q,"cwd":%q,"timestamp":"2026-08-01T13:00:00Z"}`+"\n"+
		`{"id":"p1","parentId":null,"type":"message","timestamp":"2026-08-01T13:00:01Z","message":{"role":"user","content":"question"}}`+"\n"+
		`{"id":"p2","parentId":"p1","type":"message","timestamp":"2026-08-01T13:00:02Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"answer"}]}}`+"\n",
		w.sessionID, cwd)
	path := filepath.Join(w.home, ".pi", "agent", "sessions", encodeAgentPath(cwd), "session.jsonl")
	w.fixturePath = path
	return writeFixture(path, body)
}

func (w *ingestAcceptanceWorld) seedGrokSession() error {
	return w.seedGrokSessionWithModel("")
}

func (w *ingestAcceptanceWorld) seedGrokSessionWithModel(model string) error {
	w.sessionID = grokAcceptanceSession
	cwd := filepath.Join(w.home, "workspace", "grok-project")
	root := filepath.Join(w.home, ".grok", "sessions", url.PathEscape(cwd), grokAcceptanceSession)
	if err := writeFixture(filepath.Join(root, "summary.json"), fmt.Sprintf(
		`{"info":{"id":%q,"cwd":%q},"created_at":"2026-08-01T15:00:00Z","updated_at":"2026-08-01T15:00:01Z","generated_title":"grok","current_model_id":%q}`,
		w.sessionID, cwd, model)); err != nil {
		return err
	}
	path := filepath.Join(root, "updates.jsonl")
	w.fixturePath = path
	return writeFixture(path, fmt.Sprintf(
		`{"method":"_x.ai/session/update","params":{"sessionId":%[1]q,"update":{"sessionUpdate":"turn_completed"}},"timestamp":1785596400}`+"\n"+
			`{"method":"session/update","params":{"sessionId":%[1]q,"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"question"},"_meta":{"modelId":"grok-model","promptIndex":1}}},"timestamp":1785596400}`+"\n"+
			`{"method":"session/update","params":{"sessionId":%[1]q,"_meta":{"promptId":"grok-acceptance-prompt"},"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}},"timestamp":1785596401}`+"\n",
		w.sessionID))
}

func encodeAgentPath(path string) string {
	return strings.NewReplacer(":", "-", `\`, "-", "/", "-").Replace(path)
}
