package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/toolcallobserver"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestToolCallObserverRefusesWhenTheInvokingSessionIsNotAFact(t *testing.T) {
	env := hermeticCLIEnv(&cliEnv{
		build: contractBuild(),
		observerLive: func() toolcallobserver.Evidence {
			return toolcallobserver.Evidence{Processes: []toolcallobserver.Process{{Command: "zsh"}}}
		},
	})
	_, err := executeWithEnv(env, []string{"tool-call-observer"}, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "invoking agent") {
		t.Fatalf("error = %v", err)
	}
}

func TestToolCallObserverOpensAHumanTabAndPrintsAXI(t *testing.T) {
	const path = "/synthetic/session.jsonl"
	var opened toolcallobserver.WindowRequest
	var out, errs strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: contractBuild(), out: &out, errOut: &errs,
		observerExecutable: "/synthetic/roca",
		observerResolve: func() (toolcallobserver.Session, error) {
			return toolcallobserver.Session{Harness: "claude", Kind: parsers.KindClaudeSession, Path: path, ID: "synthetic"}, nil
		},
		observerOpen: func(_ context.Context, session toolcallobserver.Session, req toolcallobserver.WindowRequest) (toolcallobserver.OpenedWindow, error) {
			opened = req
			if session.Path != path {
				t.Fatalf("window session = %q, want %q", session.Path, path)
			}
			return toolcallobserver.OpenedWindow{Kind: toolcallobserver.WindowHerdr, Workspace: toolcallobserver.HumanWorkspaceLabel, TabID: "wCap:t1"}, nil
		},
	})
	code, err := executeWithEnv(env, []string{"tool-call-observer"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	got := out.String()
	for _, fragment := range []string{"observer: Claude Code", "window: new tab in workspace " + toolcallobserver.HumanWorkspaceLabel, "help["} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("output missing %q:\n%s", fragment, got)
		}
	}
	if !strings.Contains(strings.Join(opened.Env, "\n"), observerFollowEnv+"="+path) {
		t.Fatalf("follow env not passed: %v", opened.Env)
	}
	if !strings.Contains(strings.Join(opened.Env, "\n"), observerKindEnv+"="+string(parsers.KindClaudeSession)) {
		t.Fatalf("kind env not passed: %v", opened.Env)
	}
	if opened.Command[0] != "/synthetic/roca" || opened.Command[1] != "tool-call-observer" {
		t.Fatalf("command = %v", opened.Command)
	}

	out.Reset()
	env.json = true
	code, err = executeWithEnv(env, []string{"--json", "tool-call-observer"}, nil)
	if err != nil || code != 0 {
		t.Fatalf("json code=%d err=%v", code, err)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(out.String()), &document); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if document["harness"] != "claude" || document["session_file"] != path {
		t.Fatalf("json envelope = %v", document)
	}
}

func TestToolCallObserverRefusesDatabaseBackedSessionsBeforeOpeningAWindow(t *testing.T) {
	env := hermeticCLIEnv(&cliEnv{
		build: contractBuild(),
		observerResolve: func() (toolcallobserver.Session, error) {
			return toolcallobserver.Session{Harness: "opencode", Kind: parsers.KindOpenCodeDB, Path: "/synthetic/opencode.db"}, nil
		},
		observerOpen: func(context.Context, toolcallobserver.Session, toolcallobserver.WindowRequest) (toolcallobserver.OpenedWindow, error) {
			t.Fatal("database session opened a window")
			return toolcallobserver.OpenedWindow{}, nil
		},
	})
	_, err := executeWithEnv(env, []string{"tool-call-observer"}, nil)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Fatalf("error = %v", err)
	}
}

func TestToolCallObserverFollowsWithoutOpeningAWindow(t *testing.T) {
	t.Setenv(observerFollowEnv, "/synthetic/session.jsonl")
	t.Setenv(observerHarnessEnv, "claude")
	t.Setenv(observerKindEnv, string(parsers.KindClaudeSession))
	var followed toolcallobserver.Session
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: contractBuild(), out: &out, errOut: io.Discard,
		observerFollow: func(_ context.Context, session toolcallobserver.Session, _ io.Writer, _ toolcallobserver.FollowOptions) error {
			followed = session
			return nil
		},
		observerOpen: func(context.Context, toolcallobserver.Session, toolcallobserver.WindowRequest) (toolcallobserver.OpenedWindow, error) {
			t.Fatal("follow mode opened a window")
			return toolcallobserver.OpenedWindow{}, nil
		},
	})
	if _, err := executeWithEnv(env, []string{"tool-call-observer"}, nil); err != nil {
		t.Fatal(err)
	}
	if followed.Path != "/synthetic/session.jsonl" || followed.Harness != "claude" || followed.Kind != parsers.KindClaudeSession {
		t.Fatalf("followed %+v", followed)
	}
}

func TestToolCallObserverIsAPublicCommand(t *testing.T) {
	if !publicCommand("tool-call-observer") {
		t.Fatal("tool-call-observer is hidden")
	}
	if builtIn(rootCommand(&cliEnv{}), "tool-call-observer") == false {
		t.Fatal("tool-call-observer has no CLI seat")
	}
}
