package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestAMissingFileIsNotAFailure(t *testing.T) {
	file, err := LoadFile(filepath.Join(t.TempDir(), "nothing.toml"))
	if err != nil {
		t.Fatalf("a machine with no config is a machine with defaults: %v", err)
	}
	if file.Exists {
		t.Fatal("it says a file that is not there exists")
	}
	if len(file.Models.Order) != 0 {
		t.Fatalf("order %v", file.Models.Order)
	}
}

func TestTheModelsSectionIsReadWhole(t *testing.T) {
	path := write(t, `
[models]
order = ["codex", "ollama"]
interpret_order = ["ollama"]
timeout_ms = 15000

[models.ollama]
base_url = "http://localhost:11434"
model = "qwen3.5:4b"
keep_alive = "10m"
think = true

[models.codex]
model = "gpt-test"
`)
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !file.Exists || file.Path != path {
		t.Fatalf("file %+v", file)
	}
	if got := strings.Join(file.Models.Order, ","); got != "codex,ollama" {
		t.Fatalf("order %q", got)
	}
	if got := strings.Join(file.Models.InterpretOrder, ","); got != "ollama" {
		t.Fatalf("interpret order %q", got)
	}
	if len(file.Warnings) != 0 {
		t.Fatalf("a known key was reported as unknown: %v", file.Warnings)
	}
	if file.Models.TimeoutMS != 15000 {
		t.Fatalf("timeout %d", file.Models.TimeoutMS)
	}
	codex := file.Models.Providers["codex"]
	if codex.Model != "gpt-test" {
		t.Fatalf("codex %+v", codex)
	}
	ollama := file.Models.Providers["ollama"]
	if ollama.BaseURL != "http://localhost:11434" || ollama.KeepAlive != "10m" || !ollama.Think {
		t.Fatalf("ollama %+v", ollama)
	}
	if codex.Think {
		t.Fatal("thinking is on for a provider that never asked for it")
	}
}

func TestABinaryProviderConfigurationIsReadWhole(t *testing.T) {
	path := write(t, `[models.claude]
command = ["claude", "-p", "{prompt}", "--model", "{model}", "--effort", "{effort}"]
model = "sonnet"
effort = "high"
response_format = "json"
timeout_seconds = 45
`)
	file, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := file.Models.Providers["claude"]
	if strings.Join(got.Command, " ") != "claude -p {prompt} --model {model} --effort {effort}" ||
		got.TimeoutSeconds != 45 || got.Values["model"] != "sonnet" ||
		got.Values["effort"] != "high" || got.ResponseFormat != "json" || len(file.Warnings) != 0 {
		t.Fatalf("provider = %+v, warnings = %v", got, file.Warnings)
	}
}

func TestAnExplicitCommandKeepsPresetAsCommandConfiguration(t *testing.T) {
	path := write(t, `[models.fixture]
command = ["fixture", "--profile", "{preset}", "{prompt}"]
preset = "operator-profile"
model = "fixture-model"
`)
	file, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := file.Models.Providers["fixture"]
	if cfg.RetiredCredential || cfg.Values["preset"] != "operator-profile" || len(file.Warnings) != 0 {
		t.Fatalf("provider = %+v, warnings = %v", cfg, file.Warnings)
	}
}

func TestProviderSecretRedactionHandlesEveryValidTOMLShape(t *testing.T) {
	if key, index := tomlAssignment(`model.name = "nested"`); key != "" || index != -1 {
		t.Fatalf("dotted assignment parsed as scalar %q at %d", key, index)
	}
	for _, body := range []string{
		"[models.fixture]\n\"api_key\" = \"legacy-secret\"\nmodel = \"fixture\"\n",
		"models.fixture.api_key = \"legacy-secret\"\nmodels.fixture.model = \"fixture\"\n",
		"models = { fixture = { api_key = \"legacy-secret\", model = \"fixture\" } }\n",
	} {
		redacted, err := RedactProviderSecrets(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(redacted, "legacy-secret") || strings.Contains(redacted, "api_key") {
			t.Fatalf("provider secret survived redaction:\n%s", redacted)
		}
		path := write(t, redacted)
		if file, err := LoadFile(path); err != nil || file.Models.Providers["fixture"].Model != "fixture" {
			t.Fatalf("redaction lost provider configuration: file=%+v err=%v\n%s", file, err, redacted)
		}
	}
}

func TestProviderMigrationDeduplicatesWithResolverNormalization(t *testing.T) {
	text := "[models]\norder = [\"xai\", \"Codex\", \"codex\"]\n"
	updated, err := ApplyText(text, []Change{{
		Kind: ReplaceListValue, Table: "models", Key: "order", Old: "xai", Value: "codex",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated, `order = ["codex"]`) {
		t.Fatalf("normalized duplicate survived migration:\n%s", updated)
	}
	updated, err = ApplyText("[models]\norder = [\"xai\"]\n", []Change{{
		Kind: RemoveListValue, Table: "models", Key: "order", Old: "xai",
	}})
	if err != nil || strings.Contains(updated, "order =") {
		t.Fatalf("removing the last provider left an explicit empty order: err=%v\n%s", err, updated)
	}
}

func TestALegacyHTTPAndKeyConfigIsToleratedButIgnored(t *testing.T) {
	path := write(t, `[models.fixture]
base_url = "https://example.invalid/v1"
command = ["fixture", "{prompt}"]
api_key = "legacy-secret"
`)
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("legacy configuration crashed: %v", err)
	}
	cfg := file.Models.Providers["fixture"]
	if len(cfg.Command) == 0 || !cfg.RetiredCredential || cfg.Values["api_key"] != "" {
		t.Fatalf("provider = %+v", cfg)
	}
	for _, piece := range []string{"models.fixture.base_url", "models.fixture.api_key", path} {
		if !strings.Contains(strings.Join(file.Warnings, "\n"), piece) {
			t.Errorf("warning does not name %q: %v", piece, file.Warnings)
		}
	}
}

func TestAnUnknownCommandPlaceholderNamesTheProviderKeyAndFile(t *testing.T) {
	path := write(t, `[models.fixture]
command = ["fixture", "--effort", "{effort}"]
model = "fixture-model"
`)
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("unknown placeholder was accepted")
	}
	for _, piece := range []string{"fixture", "{effort}", "models.fixture", path} {
		if !strings.Contains(err.Error(), piece) {
			t.Errorf("error does not name %q: %v", piece, err)
		}
	}
}

func TestTheQueryCostBudgetIsReadFromConfig(t *testing.T) {
	file, err := LoadFile(write(t, "[query]\ntimeout_ms = 2750\n"))
	if err != nil {
		t.Fatal(err)
	}
	if file.Query.TimeoutMS != 2750 {
		t.Fatalf("query timeout = %dms, want 2750ms", file.Query.TimeoutMS)
	}
}

func TestSetProviderModelCreatesAndSurgicallyEditsTheConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := SetProviderModel(path, "xai", "grok-first"); err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "[models.xai]\nmodel = \"grok-first\"") {
		t.Fatalf("created config does not carry the model:\n%s", first)
	}
	if err := os.WriteFile(path, []byte("[models.xai]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetProviderModel(path, "xai", "grok-after-header"); err != nil {
		t.Fatalf("header without newline: %v", err)
	}
	if file, err := LoadFile(path); err != nil || file.Models.Providers["xai"].Model != "grok-after-header" {
		t.Fatalf("header-only config was not updated: file=%+v err=%v", file, err)
	}

	before := "# operator note\nworkspace_roots = [\"/work\"]\n\n[models]\norder = [\"xai\"]\n\n[models.xai]\nbase_url = \"https://example.invalid/v1\" # keep me\nmodel = \"old\" # chosen here\napi_key_env = \"XAI_KEY\"\n"
	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetProviderModel(path, "xai", "grok-operator"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(before, "model = \"old\"", "model = \"grok-operator\"", 1)
	if string(after) != want {
		t.Fatalf("unrelated bytes changed:\n--- want ---\n%s--- got ---\n%s", want, after)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err=%v, want 0600", info, err)
	}
}

func TestEditingAConfigurationWithAnInlineKeyTightensItsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[models.xai]\nmodel = \"old\"\napi_key = \"secret\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SetProviderModel(path, "xai", "new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential-bearing config info = %v, err=%v; want mode 0600", info, err)
	}
}

func TestSetModelOrderCreatesAndSurgicallyEditsTheConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	before := "# operator note\nworkspace_roots = [\"/work\"]\n\n[models]\n# keep this too\norder = [\"codex\", \"ollama\"] # chosen here\nprobe_ms = 75\n\n[models.codex]\nmodel = \"gpt-operator\"\n"
	if err := os.WriteFile(path, []byte(before), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOrder(path, []string{"xai", "codex", "ollama"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	want := strings.Replace(before,
		"order = [\"codex\", \"ollama\"]",
		"order = [\"xai\", \"codex\", \"ollama\"]", 1)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != want {
		t.Fatalf("unrelated bytes changed:\n--- want ---\n%s--- got ---\n%s", want, after)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v err=%v, want 0600", info, err)
	}

	childOnly := "[models.xai]\nmodel = \"grok-4\"\n"
	if err := os.WriteFile(path, []byte(childOnly), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOrder(path, []string{"xai", "ollama"}); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	want = "[models]\norder = [\"xai\", \"ollama\"]\n\n" + childOnly
	if after, err := os.ReadFile(path); err != nil || string(after) != want {
		t.Fatalf("parent insertion = %q, err=%v; want %q", after, err, want)
	}

	multiline := "[models]\norder = [\n  \"codex\", # keep valid comments\n  \"ollama\",\n]\nprobe_ms = 75\n"
	if err := os.WriteFile(path, []byte(multiline), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetModelOrder(path, []string{"xai", "ollama"}); err != nil {
		t.Fatalf("multiline: %v", err)
	}
	want = "[models]\norder = [\"xai\", \"ollama\"]\nprobe_ms = 75\n"
	if after, err := os.ReadFile(path); err != nil || string(after) != want {
		t.Fatalf("multiline replacement = %q, err=%v; want %q", after, err, want)
	}
}

func TestLooseKeysAreReadOnlyFromDefaults(t *testing.T) {
	file, err := LoadFile(write(t, "model = \"ignored-root\"\n\n[defaults]\nmodel = \"configured\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := file.Default("model"); got != "configured" {
		t.Fatalf("model %q", got)
	}
	rootOnly, err := LoadFile(write(t, "model = \"ignored-root\"\n"))
	if err != nil {
		t.Fatalf("load root-only file: %v", err)
	}
	if got := rootOnly.Default("model"); got != "" {
		t.Fatalf("root-level model still resolves: %q", got)
	}
}

func TestAnUnknownKeyIsAWarningThatNamesTheKeyTheFileAndTheRemedy(t *testing.T) {
	path := write(t, "[models]\norder = [\"ollama\"]\nturbo_mode = true\n")
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("data the operator persisted is survived, not rejected: %v", err)
	}
	if len(file.Warnings) != 1 {
		t.Fatalf("expected one warning, got %v", file.Warnings)
	}
	warning := file.Warnings[0]
	for _, piece := range []string{"turbo_mode", path, "Remove that line"} {
		if !strings.Contains(warning, piece) {
			t.Errorf("the warning does not name %q: %s", piece, warning)
		}
	}
	// And what it does understand keeps working.
	if got := strings.Join(file.Models.Order, ","); got != "ollama" {
		t.Fatalf("order %q", got)
	}
}

func TestAProviderSpecificKeyBecomesATemplateValue(t *testing.T) {
	path := write(t, "[models.ollama]\nmodel = \"qwen3.5:4b\"\nteleport = 3\n")
	file, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(file.Warnings) != 0 {
		t.Fatalf("provider tuning was reported as unknown: %v", file.Warnings)
	}
	if file.Models.Providers["ollama"].Model != "qwen3.5:4b" {
		t.Fatal("the known keys stopped loading")
	}
	if file.Models.Providers["ollama"].Values["teleport"] != "3" {
		t.Fatalf("provider tuning was not preserved: %+v", file.Models.Providers["ollama"].Values)
	}
}

// Broken TOML is not survivable data: it is a file that has to be fixed, and the
// message says where.
func TestBrokenTOMLNamesTheFile(t *testing.T) {
	path := write(t, "[models\norder = 3\n")
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("broken TOML has to fail")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("the error does not name the file: %v", err)
	}
}

func TestTheConfigPathHangsOffTheDataDirectoryAndTheEnvironmentWins(t *testing.T) {
	home := t.TempDir()
	paths, err := Resolve(Input{Home: home})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(home, DirOwn, FileConfig); paths.Config != want {
		t.Fatalf("config %q, want %q", paths.Config, want)
	}

	t.Setenv(EnvConfig, "/elsewhere/roca.toml")
	paths, err = Resolve(Input{Home: home, ConfigEnv: os.Getenv(EnvConfig)})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if paths.Config != "/elsewhere/roca.toml" {
		t.Fatalf("config %q", paths.Config)
	}
}

// List keys accept every documented shape under [defaults].
func TestDefaultListReadsEveryShapeFromDefaults(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"a TOML array", "[defaults]\nworkspace_roots = [\"/w\", \"/x\"]\n", []string{"/w", "/x"}},
		{"a single path", "[defaults]\nworkspace_roots = \"/w\"\n", []string{"/w"}},
		{"a JSON array inside a string", "[defaults]\nworkspace_roots = \"[\\\"/w\\\", \\\"/x\\\"]\"\n",
			[]string{"/w", "/x"}},
	}
	for _, one := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(one.content), 0o600); err != nil {
			t.Fatalf("%s: %v", one.name, err)
		}
		file, err := LoadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", one.name, err)
		}
		got := file.DefaultList("workspace_roots")
		if len(got) != len(one.want) {
			t.Errorf("%s: %v, want %v", one.name, got, one.want)
			continue
		}
		for i := range got {
			if got[i] != one.want[i] {
				t.Errorf("%s: %v, want %v", one.name, got, one.want)
				break
			}
		}
	}
}

// A TOML header may be spelled several ways for the same table, and the operator
// wrote the file. Matching the raw line text meant `[models."xai"]` and
// `[ models.xai ]` never matched the table being edited, so the edit APPENDED a
// second `[models.xai]`: two tables for one key in the operator's own file.
func TestAnEquivalentTableHeaderIsEditedAndNotDuplicated(t *testing.T) {
	for _, header := range []string{`[models."xai"]`, `[ models.xai ]`, "[models.xai]"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		before := header + "\nmodel = \"old\"\napi_key_env = \"XAI_KEY\"\n"
		if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetProviderModel(path, "xai", "grok-new"); err != nil {
			t.Fatalf("%s: %v", header, err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		declared := 0
		for _, line := range strings.Split(string(after), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "xai") {
				declared++
			}
		}
		if declared != 1 {
			t.Errorf("%s: the table is declared %d times:\n%s", header, declared, after)
		}
		file, err := LoadFile(path)
		if err != nil {
			t.Fatalf("%s: the edited file no longer parses: %v", header, err)
		}
		if got := file.Models.Providers["xai"].Model; got != "grok-new" {
			t.Errorf("%s: model = %q, want grok-new:\n%s", header, got, after)
		}
	}
}
