package service_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestStoreRefusesAHandoffFromANonSessionWriter(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := shapedHandoff("branch-shaped worker noise")

	tests := []struct {
		name    string
		request service.StoreRequest
		agent   string
		surface string
		origin  string
	}{
		{"unknown authorship", service.StoreRequest{Layer: "handoff", Content: content},
			"unknown", "unknown", "agent"},
		{"cron origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "cron",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}, "claude", service.SurfaceCLI, "cron"},
		{"plugin origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "plugin:demo",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}, "claude", service.SurfaceCLI, "plugin:demo"},
		{"unknown surface", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet"},
		}, "claude", "unknown", "agent"},
		{"worker-named agent", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{
				Agent: "glm-5.2 (codex/slopslint-detector-a1)", Model: "glm", Surface: service.SurfaceCLI,
			},
		}, "glm-5.2 (codex/slopslint-detector-a1)", service.SurfaceCLI, "agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Store(t.Context(), test.request)
			if err == nil {
				t.Fatal("store accepted a handoff from a writer that is not a session harness")
			}
			got := err.Error()
			wantPrefix := fmt.Sprintf("handoff refused: agent=%q surface=%q origin=%q",
				test.agent, test.surface, test.origin)
			if !strings.Contains(got, wantPrefix) {
				t.Errorf("refusal does not name what it saw (%s): %v", wantPrefix, err)
			}
			for _, want := range []string{
				"session writers are", "tasks-axi", "pr", "decision", "expires_at",
				"accepted layers for this surface", "layer=discovery",
				"branch/scope:", "done:", "state:", "next:", "layer=handoff",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStoreRefusesAHandoffThatOmitsTheRequiredShape(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	tests := []struct {
		name    string
		content string
	}{
		{"unlabeled prose", "token refresh done, retry pending"},
		{"near labels", "DONE (verified): recorded\nSTATUS: stored\nSUPERSEDE 42\nbranch: fixture\nnext: continue"},
		{"next step alias", "branch: fixture\ndone: recorded\nstate: stored\nnext step: ship"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Store(t.Context(), service.StoreRequest{
				Layer: "handoff", Content: test.content, Authorship: sessionWriter(),
			})
			if err == nil {
				t.Fatal("store accepted a handoff without the accepted labels")
			}
			for _, want := range []string{
				"branch/scope:", "done:", "state:", "current state:", "next:",
				"SUPERSEDE", "--supersedes",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("shape refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStoreRefusesAHandoffWithBlankLabeledFields(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	_, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "branch: done: state: next:", Authorship: sessionWriter(),
	})
	if err == nil {
		t.Fatal("store accepted blank handoff fields")
	}
	for _, want := range []string{"branch/scope", "done", "state", "next"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("blank-field refusal does not name %q: %v", want, err)
		}
	}
}

func TestStoreAcceptsASessionHandoffWithTheRequiredShape(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	result, err := svc.Store(t.Context(), sessionHandoff("the session closed on this branch"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ID == 0 || result.Layer != "handoff" {
		t.Fatalf("accepted write = %+v", result)
	}
}

func TestCanonicalSessionAgentMapsRuntimeAliases(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"claude-desktop", "claude"},
		{"cowork", "claude"},
		{"Claude Code", "claude"},
		{"claude-ai", "claude"},
		{"claude-code", "claude"},
		{"codex", "codex"},
		{"Codex CLI", "codex"},
		{"hermes-agent", "hermes"},
		{"ZCode", "zcode"},
		{"cursor-ide", "cursor"},
		{"qwen-code", "qwen"},
		{"glm-5.2 (codex/slopslint-detector-a1)", "glm-5.2 (codex/slopslint-detector-a1)"},
		{"", ""},
	}
	for _, test := range tests {
		if got := service.CanonicalSessionAgent(test.in); got != test.want {
			t.Errorf("CanonicalSessionAgent(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}

func TestStoreAcceptsHandoffsFromAliasedSessionWriters(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := shapedHandoff("aliased session writer")
	for _, agent := range []string{
		"claude-desktop", "cowork", "Claude Code", "claude-ai", "codex",
		"ZCode", "zcode", "opencode", "hermes", "pi", "cursor", "grok", "qwen",
	} {
		t.Run(agent, func(t *testing.T) {
			result, err := svc.Store(t.Context(), service.StoreRequest{
				Layer: "handoff", Content: content + "\n" + agent,
				Authorship: service.Authorship{Agent: agent, Model: "sonnet", Surface: service.SurfaceMCP},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ID == 0 || result.Layer != "handoff" {
				t.Fatalf("accepted write = %+v", result)
			}
		})
	}
}

func TestMCPLegacyAliasRetryIsTheSameMemory(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := "legacy MCP alias retry fixture"
	first, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "Claude Code", Model: "sonnet", Surface: service.SurfaceMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceMCP},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Skipped || retry.ID != first.ID {
		t.Fatalf("MCP alias retry = %+v, want skipped id %d", retry, first.ID)
	}
}

func TestCLIHarnessNamesStayDistinctAuthors(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	content := "cli harness names stay distinct"
	first, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude-code", Model: "sonnet", Surface: service.SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "discovery", Content: content,
		Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Skipped || second.ID == first.ID {
		t.Fatalf("CLI harness names collapsed: first=%d second=%+v", first.ID, second)
	}
}

func TestStoreAppliesTheHandoffPolicyThroughTheHandoverAlias(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	_, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handover", Content: "an alias of handoff",
	})
	if err == nil {
		t.Fatal("handover alias skipped the handoff writer policy")
	}
}

func sessionWriter() service.Authorship {
	return service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI}
}

func shapedHandoff(body string) string {
	return strings.TrimSpace(body) + "\nbranch: fixture\ndone: recorded\nstate: stored\nnext: continue\n"
}

func sessionHandoff(body string) service.StoreRequest {
	return service.StoreRequest{
		Layer: "handoff", Content: shapedHandoff(body), Authorship: sessionWriter(),
	}
}
