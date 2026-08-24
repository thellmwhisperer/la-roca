package toolcallobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestChooseWorkspacePrefersTheHumanFacingLabel(t *testing.T) {
	human := Workspace{ID: "wCap", Label: HumanWorkspaceLabel, Focused: false}
	current := Workspace{ID: "wCur", Label: "agent-lab", Focused: true}
	got, reason := ChooseWorkspace([]Workspace{current, human}, "wCur")
	if got.ID != "wCap" || reason != HumanWorkspaceLabel {
		t.Fatalf("got %+v %q, want the human-facing workspace", got, reason)
	}

	got, reason = ChooseWorkspace([]Workspace{current}, "wCur")
	if got.ID != "wCur" || reason != "current" {
		t.Fatalf("absent human workspace: got %+v %q, want the current workspace", got, reason)
	}
}

func TestOpenWindowUsesAHumanTabWhenHerdrAnswers(t *testing.T) {
	var ran [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "herdr" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		ran = append(ran, append([]string{name}, args...))
		switch {
		case len(args) >= 2 && args[0] == "workspace" && args[1] == "list":
			return herdrListJSON(t, []Workspace{
				{ID: "wCap", Label: HumanWorkspaceLabel},
				{ID: "wCur", Label: "agent-lab", Focused: true},
			}), nil
		case len(args) >= 2 && args[0] == "tab" && args[1] == "create":
			return []byte(`{"result":{"tab":{"tab_id":"wCap:t9"},"root_pane":{"pane_id":"wCap:p9"}}}`), nil
		case len(args) >= 2 && args[0] == "pane" && args[1] == "run":
			return []byte(`{"result":{}}`), nil
		default:
			return nil, fmt.Errorf("unexpected herdr %v", args)
		}
	}
	opened, err := OpenWindow(context.Background(), Session{Harness: "claude", Path: "/synthetic/session.jsonl"}, WindowRequest{
		Runner:   runner,
		LookPath: func(string) (string, bool) { return "/bin/herdr", true },
		Command:  []string{"/bin/roca", "tool-call-observer"},
		Env:      []string{"ROCA_TOOL_CALL_OBSERVER_FOLLOW=/synthetic/session.jsonl"},
		Cwd:      "/synthetic/lab",
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Kind != WindowHerdr || opened.Workspace != HumanWorkspaceLabel || opened.TabID != "wCap:t9" {
		t.Fatalf("opened %+v", opened)
	}
	joined := fmt.Sprintf("%v", ran)
	if !strings.Contains(joined, "--workspace wCap") {
		t.Fatalf("tab create did not target the human-facing workspace: %s", joined)
	}
}

func TestOpenWindowFallsBackToTheTerminal(t *testing.T) {
	cases := []struct {
		name     string
		lookPath func(string) (string, bool)
		runner   CommandRunner
	}{
		{
			name:     "herdr is not on PATH",
			lookPath: func(string) (string, bool) { return "", false },
		},
		{
			name:     "herdr does not answer",
			lookPath: func(string) (string, bool) { return "/bin/herdr", true },
			runner: func(context.Context, string, ...string) ([]byte, error) {
				return nil, fmt.Errorf("herdr did not answer")
			},
		},
		{
			name:     "herdr tab create omits the root pane id",
			lookPath: func(string) (string, bool) { return "/bin/herdr", true },
			runner: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if len(args) >= 2 && args[0] == "workspace" && args[1] == "list" {
					return herdrListJSON(t, []Workspace{{ID: "wCap", Label: HumanWorkspaceLabel}}), nil
				}
				if len(args) >= 2 && args[0] == "tab" && args[1] == "create" {
					return []byte(`{"result":{"tab":{"tab_id":"wCap:t9"}}}`), nil
				}
				return nil, fmt.Errorf("unexpected herdr %q %v", name, args)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var term TerminalRequest
			opened, err := OpenWindow(context.Background(), Session{Harness: "grok"}, WindowRequest{
				LookPath: test.lookPath,
				Runner:   test.runner,
				Command:  []string{"/bin/roca", "tool-call-observer"},
				OpenTerminal: func(req TerminalRequest) error {
					term = req
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if opened.Kind != WindowTerminal || len(term.Command) == 0 {
				t.Fatalf("opened %+v term %+v", opened, term)
			}
		})
	}
}

func herdrListJSON(t *testing.T, workspaces []Workspace) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"result": map[string]any{"workspaces": workspaces},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
