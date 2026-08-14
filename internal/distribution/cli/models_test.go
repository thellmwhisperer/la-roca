package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `roca models` lists every configured provider's catalogue and marks the model
// the cascade would actually use. It does not need a database: it is a question
// about providers, like model check, so an operator can run it before init.
func TestModelsListsEachProviderAndMarksTheSelected(t *testing.T) {
	home := isolatedLoginHome(t)
	writeCommandProviderConfig(t, home, "mycorp", "mycorp-7b", true)

	out := runRoot(t, Build{Version: "test", Commit: "abc123"}, "models")

	for _, want := range []string{"[ok] mycorp", "mycorp-7b (selected)"} {
		if !strings.Contains(out, want) {
			t.Errorf("models output missing %q:\n%s", want, out)
		}
	}
}

// A provider that does not answer is listed as unavailable with its reason, and
// the command keeps going instead of aborting on the first failure.
func TestModelsKeepsGoingPastAProviderThatDoesNotAnswer(t *testing.T) {
	home := isolatedLoginHome(t)
	writeCommandProviderConfig(t, home, "alpha", "alpha-1", false)

	out := runRoot(t, Build{Version: "test"}, "models")
	if !strings.Contains(out, "[no] alpha") {
		t.Fatalf("an unreachable provider must be listed as not ready:\n%s", out)
	}
	if !strings.Contains(out, "alpha-agent binary not found in PATH") {
		t.Fatalf("the reason must name where it looked:\n%s", out)
	}
}

// The --json envelope carries version and source_sha like every other command,
// and each provider reports its catalogue and its selected model.
func TestModelsJSONContract(t *testing.T) {
	home := isolatedLoginHome(t)
	writeCommandProviderConfig(t, home, "mycorp", "mycorp-7b", true)

	out := runRoot(t, Build{Version: "test", Commit: "abc123"}, "--json", "models")
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if result["version"] != "test" || result["source_sha"] != "abc123" {
		t.Fatalf("the envelope lost its build stamps: %#v", result)
	}
	providers, ok := result["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("expected one provider, got %#v", result["providers"])
	}
	row, _ := providers[0].(map[string]any)
	if row["provider"] != "mycorp" || row["selected"] != "mycorp-7b" {
		t.Fatalf("the provider row lost its name or selected model: %#v", row)
	}
	if ready, _ := row["ready"].(bool); !ready {
		t.Fatalf("a reachable provider must be ready: %#v", row)
	}
	models, _ := row["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("the catalogue did not travel: %#v", models)
	}
	if warnings, ok := result["warnings"].([]any); !ok || len(warnings) != 0 {
		t.Fatalf("a clean configuration must still answer with an empty warning list: %#v", result["warnings"])
	}
	if reason, ok := result["reason"].(string); !ok || reason != "" {
		t.Fatalf("a cascade that listed providers carries no empty-cascade reason: %#v", result["reason"])
	}
}

// An empty catalogue names which empty cascade it is on both surfaces, the same
// distinction `model check` makes: an order that declared nothing is not an
// order whose every entry this build had to drop, and a warning about a key the
// order never named tells the two apart for neither. The machine answer carries
// that reason as its own field, because an empty provider list plus a warning
// list is exactly what cannot tell the two causes apart.
func TestModelsNamesWhichEmptyCascadeItIs(t *testing.T) {
	for _, test := range []struct{ name, envOrder, file, want string }{
		{name: "the order is turned off", envOrder: "none", want: "no provider is declared"},
		{
			name: "an empty order beside a warning about a provider it never named",
			file: "[models]\norder = []\n\n[models.codex]\napi_key = \"synthetic-not-a-key\"\n",
			want: "no provider is declared",
		},
		{
			name: "every declared provider was dropped", file: "[models]\norder = [\"nosuch\"]\n",
			want: "no declared provider can be used by this build",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := isolatedLoginHome(t)
			if test.envOrder != "" {
				t.Setenv("ROCA_MODELS_ORDER", test.envOrder)
			}
			if test.file != "" {
				writeConfig(t, home, test.file)
			}
			out := runRoot(t, Build{Version: "test"}, "models")
			if !strings.Contains(out, test.want) {
				t.Fatalf("models output = %q, want %q", out, test.want)
			}
			var result map[string]any
			machine := runRoot(t, Build{Version: "test"}, "--json", "models")
			if err := json.Unmarshal([]byte(machine), &result); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, machine)
			}
			if providers, _ := result["providers"].([]any); len(providers) != 0 {
				t.Fatalf("an empty cascade lists no provider: %#v", result["providers"])
			}
			if result["reason"] != test.want {
				t.Fatalf("models --json reason = %#v, want %q", result["reason"], test.want)
			}
		})
	}
}

// writeCommandProviderConfig declares one local agent command provider.
func writeCommandProviderConfig(t *testing.T, home, name, model string, available bool) {
	t.Helper()
	command := name + "-agent"
	if available {
		if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), command),
			[]byte("#!/bin/sh\nprintf 'SELECT 1\n'\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, home, fmt.Sprintf(
		"[models]\norder = [%q]\n\n[models.%s]\ncommand = [%q, \"{prompt}\"]\nmodel = %q\n",
		name, name, command, model))
}

// writeConfig lays a configuration under an isolated home and says where it
// wrote it, which is what every test that reads it back needs next.
func writeConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The interpretation order reaches doctor from the configuration file: the two
// inferences are split, and doctor names the provider the result rows go to
// with the same verdict it gives the one that answers the question.
func TestDoctorReportsTheConfiguredInterpretationProvider(t *testing.T) {
	home := isolatedLoginHome(t)
	writeConfig(t, home, "[models]\norder = [\"mycorp\"]\ninterpret_order = [\"ollama\"]\nexplore_order = [\"claude\"]\nprobe_ms = 200\n"+
		"\n[models.mycorp]\ncommand = [\"missing-mycorp-cli\", \"{prompt}\"]\nmodel = \"mycorp-7b\"\n"+
		"\n[models.ollama]\nbase_url = \"http://127.0.0.1:1\"\nmodel = \"qwen3.5:4b\"\n")

	build := Build{Version: "test", Commit: "abc123"}
	runRoot(t, build, "init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	out := runRoot(t, build, "doctor")

	for _, want := range []string{
		"interpretation providers, in the declared order:",
		"[no] ollama",
		"remedy: start the local model with `ollama serve`",
		"no interpretation provider is available: the result rows fall back to",
		"deep exploration providers, in the declared order:",
		"[no] claude",
		"no deep exploration provider is available: deep mode falls back to interpretation order, then main order",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor does not report the interpretation decision (%q):\n%s", want, out)
		}
	}
}
