package service_test

import (
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
	}{
		{"unknown authorship", service.StoreRequest{Layer: "handoff", Content: content}},
		{"cron origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "cron",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}},
		{"plugin origin", service.StoreRequest{
			Layer: "handoff", Content: content, Origin: "plugin:demo",
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet", Surface: service.SurfaceCLI},
		}},
		{"unknown surface", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{Agent: "claude", Model: "sonnet"},
		}},
		{"worker-named agent", service.StoreRequest{
			Layer: "handoff", Content: content,
			Authorship: service.Authorship{
				Agent: "glm-5.2 (codex/slopslint-detector-a1)", Model: "glm", Surface: service.SurfaceCLI,
			},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := svc.Store(t.Context(), test.request)
			if err == nil {
				t.Fatal("store accepted a handoff from a writer that is not a session harness")
			}
			for _, want := range []string{"tasks-axi", "pr", "decision", "expires_at"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not name %q: %v", want, err)
				}
			}
		})
	}
}

func TestStoreRefusesAHandoffThatOmitsTheRequiredShape(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	_, err := svc.Store(t.Context(), service.StoreRequest{
		Layer: "handoff", Content: "token refresh done, retry pending",
		Authorship: sessionWriter(),
	})
	if err == nil {
		t.Fatal("store accepted a handoff without branch, done, state and next")
	}
	for _, want := range []string{"branch", "done", "state", "next", "supersedes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("shape refusal does not name %q: %v", want, err)
		}
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
