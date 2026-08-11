package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `roca models` lists every configured provider's catalogue and marks the model
// the cascade would actually use. It does not need a database: it is a question
// about providers, like login, so an operator can run it before init.
func TestModelsListsEachProviderAndMarksTheSelected(t *testing.T) {
	home := isolatedLoginHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"mycorp-7b"},{"id":"mycorp-mini"}]}`)
	}))
	defer server.Close()
	writeProviderConfig(t, home, "mycorp", server.URL, "mycorp-7b")

	out := runRoot(t, Build{Version: "test", Commit: "abc123"}, "models")

	for _, want := range []string{"[ok] mycorp", "mycorp-7b (selected)", "mycorp-mini"} {
		if !strings.Contains(out, want) {
			t.Errorf("models output missing %q:\n%s", want, out)
		}
	}
}

// A provider that does not answer is listed as unavailable with its reason, and
// the command keeps going instead of aborting on the first failure.
func TestModelsKeepsGoingPastAProviderThatDoesNotAnswer(t *testing.T) {
	home := isolatedLoginHome(t)
	writeProviderConfig(t, home, "alpha", "http://127.0.0.1:1", "alpha-1")

	out := runRoot(t, Build{Version: "test"}, "models")
	if !strings.Contains(out, "[no] alpha") {
		t.Fatalf("an unreachable provider must be listed as not ready:\n%s", out)
	}
	if !strings.Contains(out, "127.0.0.1:1") {
		t.Fatalf("the reason must name where it looked:\n%s", out)
	}
}

// The --json envelope carries version and source_sha like every other command,
// and each provider reports its catalogue and its selected model.
func TestModelsJSONContract(t *testing.T) {
	home := isolatedLoginHome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"mycorp-7b"},{"id":"mycorp-mini"}]}`)
	}))
	defer server.Close()
	writeProviderConfig(t, home, "mycorp", server.URL, "mycorp-7b")

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
	if len(models) != 2 {
		t.Fatalf("the catalogue did not travel: %#v", models)
	}
}

// writeProviderConfig declares a single OpenAI-compatible provider of the
// operator's own, named and ordered, so the cascade builds it from the file the
// way a real installation would.
func writeProviderConfig(t *testing.T, home, name, baseURL, model string) {
	t.Helper()
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[models]\norder = [%q]\n\n[models.%s]\nbase_url = %q\napi_key = \"sk-test\"\nmodel = %q\n",
		name, name, baseURL, model)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The interpretation order reaches doctor from the configuration file: the two
// inferences are split, and doctor names the provider the result rows go to
// with the same verdict it gives the one that answers the question.
func TestDoctorReportsTheConfiguredInterpretationProvider(t *testing.T) {
	home := isolatedLoginHome(t)
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "[models]\norder = [\"mycorp\"]\ninterpret_order = [\"ollama\"]\nprobe_ms = 200\n" +
		"\n[models.mycorp]\nbase_url = \"http://127.0.0.1:1/v1\"\napi_key = \"sk-test\"\nmodel = \"mycorp-7b\"\n" +
		"\n[models.ollama]\nbase_url = \"http://127.0.0.1:1\"\nmodel = \"qwen3.5:4b\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	build := Build{Version: "test", Commit: "abc123"}
	runRoot(t, build, "init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	out := runRoot(t, build, "doctor")

	for _, want := range []string{
		"interpretation providers, in the declared order:",
		"[no] ollama",
		"remedy: start the local model with `ollama serve`",
		"no interpretation provider is available: the result rows fall back to",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor does not report the interpretation decision (%q):\n%s", want, out)
		}
	}
}
