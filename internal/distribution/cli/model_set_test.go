package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// modelConfigPath is the config path under an isolated home. A helper and not an
// inline join because four tests read it, and a fifth spelling of it is a clone.
func modelConfigPath(home string) string {
	return filepath.Join(home, ".roca", "config.toml")
}

// `roca model set codex gpt-5.6-sol` writes models.codex.model and leaves every
// unrelated setting where it was. The surgical edit is config.SetProviderModel's
// own contract; this pins that the command reaches it, persists the model and
// keeps the operator's comment and the loose key the edit never touched.
func TestModelSetWritesTheModelAndPreservesTheRest(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	writeFile(t, path, "# keep this note\nworkspace_roots = [\"/work\"]\n")

	out := runRoot(t, Build{Version: "test"}, "model", "set", "codex", "gpt-5.6-sol")
	if !strings.Contains(out, "gpt-5.6-sol") {
		t.Fatalf("narration lost the model:\n%s", out)
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if file.Models.Providers["codex"].Model != "gpt-5.6-sol" {
		t.Fatalf("codex model not persisted: %+v", file.Models.Providers["codex"])
	}
	// The order is not this command's concern and the unrelated settings survive:
	// the surgical edit changed one model and nothing else.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# keep this note", `workspace_roots = ["/work"]`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the edit lost %q:\n%s", want, raw)
		}
	}
	if strings.Contains(string(raw), "order") {
		t.Fatalf("model set wrote an order it was not asked for:\n%s", raw)
	}
}

// The exact human line: the provider, the model and where it came from. The
// source is the config file because that is where the command just put it.
func TestModelSetNarratesTheModelAndItsSource(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	out := runRoot(t, Build{Version: "test"}, "model", "set", "ollama", "qwen3.5:4b")
	want := "ollama model set to qwen3.5:4b (from " + path + ")"
	if out != want {
		t.Fatalf("model set narration changed:\n--- want ---\n%s--- got ---\n%s", want, out)
	}
	if file, err := config.LoadFile(path); err != nil || file.Models.Providers["ollama"].Model != "qwen3.5:4b" {
		t.Fatalf("ollama model not persisted: file=%+v err=%v", file, err)
	}
}

// Under --json stdout carries only the result envelope: provider, model, source
// and the configuration path that was written.
func TestModelSetAnswersAJSONEnvelope(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	out := runRoot(t, Build{Version: "test"}, "--json", "model", "set", "codex", "gpt-5.6-sol")
	doc := mustJSON(t, out)
	if doc["provider"] != "codex" {
		t.Errorf("provider = %v, want codex", doc["provider"])
	}
	if doc["model"] != "gpt-5.6-sol" {
		t.Errorf("model = %v, want gpt-5.6-sol", doc["model"])
	}
	if doc["source"] != "from "+path {
		t.Errorf("source = %v, want from %s", doc["source"], path)
	}
	if doc["configuration"] != path {
		t.Errorf("configuration = %v, want %s", doc["configuration"], path)
	}
}

// A provider the operator declared a table for is a valid target even though it
// is not a built-in name: the table is what makes it known.
func TestModelSetAcceptsACustomDeclaredProvider(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	writeFile(t, path, "[models]\norder = [\"mycorp\"]\n\n[models.mycorp]\nbase_url = \"https://llm.invalid/v1\"\nmodel = \"internal-7b\"\n")

	out := runRoot(t, Build{Version: "test"}, "model", "set", "mycorp", "internal-9b")
	if !strings.Contains(out, "internal-9b") {
		t.Fatalf("custom provider model not narrated:\n%s", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "model = \"internal-9b\"") {
		t.Fatalf("custom provider model not persisted:\n%s", raw)
	}
	if !strings.Contains(string(raw), "base_url = \"https://llm.invalid/v1\"") {
		t.Fatalf("an unrelated key was lost:\n%s", raw)
	}
}

// An unknown provider fails by name, lists the ones this build knows, and points
// at `roca doctor` to inspect the configuration rather than a command that does
// not exist.
func TestModelSetUnknownProviderListsKnownAndSuggestsDoctor(t *testing.T) {
	_ = isolatedLoginHome(t)
	_, err := runRootErr(t, Build{Version: "test"}, nil, "model", "set", "nope", "x")
	if err == nil {
		t.Fatal("an unknown provider has to fail")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, `there is no provider "nope"`) {
		t.Errorf("error does not name the unknown provider upfront: %v", err)
	}
	for _, name := range []string{"codex", "ollama", "deepseek", "zai", "xai"} {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not list %q:\n%s", name, msg)
		}
	}
	if !strings.Contains(msg, "roca doctor") {
		t.Errorf("error does not point at roca doctor:\n%s", msg)
	}
}

// Errors always use the error channel, even when --json was requested.
func TestModelSetUnknownProviderListsKnownOnJSON(t *testing.T) {
	_ = isolatedLoginHome(t)
	var out strings.Builder
	env := &cliEnv{build: Build{Version: "test"}, out: &out, errOut: &out}
	root := rootCommand(env)
	root.SetArgs([]string{"--json", "model", "set", "nope", "x"})
	err := root.Execute()
	if err == nil || !strings.HasPrefix(err.Error(), `there is no provider "nope"`) {
		t.Fatalf("error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("error polluted stdout: %q", out.String())
	}
}
