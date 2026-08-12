package cli

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestCLIAuthorshipDetectionIsConservative(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		processes []authorshipProcess
		wantAgent string
		wantModel string
	}{
		{"claude marker", map[string]string{"CLAUDECODE": "1"}, nil, "claude", service.UnknownAuthor},
		{"codex marker", map[string]string{"CODEX_THREAD_ID": "thread-1"}, nil, "codex", service.UnknownAuthor},
		{"opencode ancestry", nil, []authorshipProcess{{Command: "/opt/bin/opencode"}}, "opencode", service.UnknownAuthor},
		{"pi ancestry and model", nil, []authorshipProcess{{Command: "pi", Arguments: []string{"--model=openai/gpt-5"}}}, "pi", "openai/gpt-5"},
		{"hermes marker and model", map[string]string{"HERMES_SESSION_ID": "session-1", "HERMES_INFERENCE_MODEL": "qwen3"}, nil, "hermes", "qwen3"},
		{"model in ancestry", nil, []authorshipProcess{{Command: "claude", Arguments: []string{"--model", "sonnet"}}}, "claude", "sonnet"},
		{"conflicting models", map[string]string{"HERMES_SESSION_ID": "session-1", "HERMES_INFERENCE_MODEL": "qwen3"}, []authorshipProcess{{Command: "hermes", Arguments: []string{"--model", "llama"}}}, "hermes", service.UnknownAuthor},
		{"no evidence", nil, nil, service.UnknownAuthor, service.UnknownAuthor},
		{"conflicting evidence", map[string]string{"CODEX_THREAD_ID": "thread-1"}, []authorshipProcess{{Command: "pi", Arguments: []string{"--model", "gpt-5"}}}, service.UnknownAuthor, service.UnknownAuthor},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resolveCLIAuthorship("", "", func() authorshipEvidence {
				return authorshipEvidence{Environment: test.env, Processes: test.processes}
			})
			if got.Agent != test.wantAgent || got.Model != test.wantModel || got.Surface != service.SurfaceCLI {
				t.Errorf("authorship = %+v, want agent=%q model=%q surface=cli", got, test.wantAgent, test.wantModel)
			}
		})
	}
}

func TestCLIAuthorshipFlagsOverrideDetection(t *testing.T) {
	probes := 0
	evidence := func() authorshipEvidence {
		probes++
		return authorshipEvidence{Environment: map[string]string{"CODEX_THREAD_ID": "thread-1"}}
	}
	got := resolveCLIAuthorship("opencode", "chosen-model", evidence)
	if got != (service.Authorship{Agent: "opencode", Model: "chosen-model", Surface: service.SurfaceCLI}) {
		t.Fatalf("authorship = %+v: explicit flags did not win", got)
	}
	// Both flags decide the answer on their own, so the process-ancestry walk
	// that every `roca store` would otherwise pay for is never started.
	if probes != 0 {
		t.Errorf("detection ran %d times although both identity flags were explicit", probes)
	}
	if got := resolveCLIAuthorship("opencode", "", evidence); got.Model != service.UnknownAuthor || probes != 1 {
		t.Errorf("a missing model flag did not fall back to detection: %+v after %d probes", got, probes)
	}

	command := storeCommand(&cliEnv{})
	for _, flag := range []string{"agent", "model"} {
		if command.Flags().Lookup(flag) == nil {
			t.Errorf("roca store has no --%s flag", flag)
		}
	}
}
