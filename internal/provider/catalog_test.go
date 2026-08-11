package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func settings(t *testing.T, body string) Settings {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return Settings{
		File:        file,
		Credentials: filepath.Join(dir, "credentials"),
		Env:         func(string) string { return "" },
	}
}

func pathWithBinaries(t *testing.T, names ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

func TestWithNoConfigTheOrderIsTheDefaultOne(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cascade, err := BuildCascade(settings(t, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "ollama" {
		t.Fatalf("order %q", got)
	}
}

func TestZeroConfigBuildsTheOrderFromDetectedCommandPresets(t *testing.T) {
	for _, tc := range []struct {
		name, detected, want, missing string
	}{
		{name: "both binaries", detected: "claude,codex", want: "claude,codex,ollama"},
		{name: "codex only", detected: "codex", want: "codex,ollama", missing: "claude"},
		{name: "none", want: "ollama", missing: "claude,codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binaries := []string(nil)
			if tc.detected != "" {
				binaries = strings.Split(tc.detected, ",")
			}
			t.Setenv("PATH", pathWithBinaries(t, binaries...))
			cascade, err := BuildCascade(settings(t, ""))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(names(cascade.Providers), ","); got != tc.want {
				t.Fatalf("order = %q, want %q", got, tc.want)
			}
			if got := strings.Join(cascade.DetectedBinaries, ","); got != tc.detected {
				t.Fatalf("detected binaries = %q, want %q", got, tc.detected)
			}
			var missing []string
			for _, attempt := range cascade.FallbackDiagnostics {
				missing = append(missing, attempt.Name)
				if attempt.Reason != attempt.Name+" binary not found in PATH" {
					t.Fatalf("%s reason = %q", attempt.Name, attempt.Reason)
				}
			}
			if got := strings.Join(missing, ","); got != tc.missing {
				t.Fatalf("missing binaries = %q, want %q", got, tc.missing)
			}
		})
	}
}

func TestExplicitOrderWinsUntouchedWhenCommandBinariesAreDetected(t *testing.T) {
	t.Setenv("PATH", pathWithBinaries(t, NameClaude, NameCodex))
	cascade, err := BuildCascade(settings(t, "[models]\norder = [\"ollama\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "ollama" {
		t.Fatalf("explicit order changed to %q", got)
	}
	if cascade.FactoryDefault || len(cascade.FallbackDiagnostics) != 0 {
		t.Fatalf("explicit order carries factory-default state: %+v", cascade)
	}

	t.Setenv("PATH", t.TempDir())
	cascade, err = BuildCascade(settings(t, "[models]\norder = [\"deepseek\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "deepseek" {
		t.Fatalf("explicit order changed to %q", got)
	}
	var reasons []string
	for _, diagnostic := range cascade.FallbackDiagnostics {
		reasons = append(reasons, diagnostic.Reason)
	}
	if got := strings.Join(reasons, ","); got != "claude binary not found in PATH,codex binary not found in PATH" {
		t.Fatalf("missing binary diagnostics = %q", got)
	}
}

func TestTheConfiguredOrderIsWhatDecides(t *testing.T) {
	cascade, err := BuildCascade(settings(t, `
[models]
order = ["deepseek", "ollama"]

[models.deepseek]
api_key = "sk-x"
`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "deepseek,ollama" {
		t.Fatalf("order %q", got)
	}
}

// An unknown provider in persisted configuration does not stop the cascade.
func TestAnUnknownProviderInTheConfigWarnsAndTheRestKeepWorking(t *testing.T) {
	cascade, err := BuildCascade(settings(t, "[models]\norder = [\"telepathy\", \"ollama\"]\n"))
	if err != nil {
		t.Fatalf("an unknown provider must not be a failure: %v", err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "ollama" {
		t.Fatalf("order %q", got)
	}
	joined := strings.Join(cascade.Warnings, " ")
	if !strings.Contains(joined, "telepathy") {
		t.Fatalf("the warning does not name the unknown provider: %v", cascade.Warnings)
	}
	if !strings.Contains(joined, "ollama") {
		t.Fatalf("the warning does not list the available providers: %v", cascade.Warnings)
	}
}

// A provider with its own table and its own base_url is a provider this build
// knows how to build even without a preset: one adapter, many providers.
func TestAProviderOfTheOperatorsOwnIsBuiltFromItsTable(t *testing.T) {
	cascade, err := BuildCascade(settings(t, `
[models]
order = ["mycorp"]

[models.mycorp]
base_url = "https://llm.internal/v1"
model = "internal-7b"
api_key = "k"
`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(cascade.Providers) != 1 || cascade.Providers[0].Name() != "mycorp" {
		t.Fatalf("providers %v", names(cascade.Providers))
	}
	if cascade.Providers[0].ModelID() != "internal-7b" {
		t.Fatalf("model %q", cascade.Providers[0].ModelID())
	}
}

func TestClaudeIsABuiltInLocalBinaryProvider(t *testing.T) {
	base := settings(t, "[models]\norder = [\"claude\"]\n")
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(cascade.Providers) != 1 {
		t.Fatalf("providers = %v", names(cascade.Providers))
	}
	claude, ok := cascade.Providers[0].(*LocalBinary)
	preset := commandPresets[NameClaude]
	if !ok || claude.ModelID() != preset.Model || preset.ResponseFormat != binaryResponseJSON {
		t.Fatalf("Claude provider = %#v", cascade.Providers[0])
	}
	joined := strings.Join(preset.Command, " ")
	if strings.Contains(joined, "{prompt}") {
		t.Fatalf("default Claude command exposes the prompt in argv: %q", joined)
	}
	for _, flag := range []string{
		"-p", "--output-format json", "--model {model}", "--safe-mode",
		"--strict-mcp-config", "--tools ", "--disable-slash-commands",
		"--no-session-persistence", "--no-chrome",
	} {
		if !strings.Contains(joined, flag) {
			t.Errorf("default Claude command does not contain %q: %q", flag, joined)
		}
	}
}

func TestCodexIsAZeroconfigBuiltInLocalBinaryProvider(t *testing.T) {
	base := settings(t, "[models]\norder = [\"codex\"]\n")
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatal(err)
	}
	binary, ok := cascade.Providers[0].(*LocalBinary)
	preset := commandPresets[NameCodex]
	if !ok || binary.ModelID() != DefaultCodexModel || preset.Model != DefaultCodexModel {
		t.Fatalf("Codex provider = %#v; preset = %#v", cascade.Providers[0], preset)
	}
	joined := strings.Join(preset.Command, " ")
	for _, flag := range []string{"codex exec", "--model {model}", "--sandbox read-only", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "--color never"} {
		if !strings.Contains(joined, flag) {
			t.Errorf("default Codex command does not contain %q: %q", flag, joined)
		}
	}
}

func TestAConfiguredCommandSelectsTheBinaryTransport(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		timeout     int
	}{
		{name: "fixture", model: "fixture-model", timeout: 7},
		{name: NameCodex, model: "codex-cli-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := settings(t, "[models]\norder = [\""+tc.name+"\"]\n")
			base.File.Models.Providers[tc.name] = config.ProviderConfig{
				Command: []string{fakeBinary(t), "--model", "{model}"},
				Model:   tc.model, TimeoutSeconds: tc.timeout,
			}
			base.RunnerDir = t.TempDir()
			cascade, err := BuildCascade(base)
			if err != nil {
				t.Fatal(err)
			}
			binary, ok := cascade.Providers[0].(*LocalBinary)
			if !ok || binary.ModelID() != tc.model {
				t.Fatalf("provider = %#v", cascade.Providers[0])
			}
			if tc.timeout > 0 && binary.RequestTimeout() != time.Duration(tc.timeout)*time.Second {
				t.Fatalf("timeout = %v", binary.RequestTimeout())
			}
		})
	}
}

func TestThePresetsAreInTheCatalogWithoutBeingDeclared(t *testing.T) {
	for _, name := range PresetNames() {
		cascade, err := BuildCascade(settings(t, "[models]\norder = [\""+name+"\"]\n"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cascade.Providers) != 1 {
			t.Fatalf("%s: providers %v, warnings %v", name, names(cascade.Providers), cascade.Warnings)
		}
	}
}

func TestTheCredentialIsReadFromTheEnvironmentWhenItIsNotWrittenDown(t *testing.T) {
	base := settings(t, "[models]\norder = [\"deepseek\"]\n")
	base.Env = func(key string) string {
		if key == "DEEPSEEK_API_KEY" {
			return "sk-from-the-environment"
		}
		return ""
	}
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// With a credential it is no longer "there is no credential": it fails, if
	// it fails, on the network, which is the next question.
	readiness := cascade.Providers[0].Ready(t.Context())
	if strings.Contains(readiness.Reason, "no credential") {
		t.Fatalf("it did not read the environment: %+v", readiness)
	}
}

// GLM and Grok are the model families an operator reaches for, and the key may
// be exported under that name rather than the vendor's (z.ai, xAI): both
// spellings have to work so the operator never has to learn the vendor's name.
func TestTheModelFamilyAliasIsReadForTheKeyProviders(t *testing.T) {
	for _, tc := range []struct{ name, alias string }{
		{NameZAI, "ROCA_GLM_API_KEY"},
		{NameXAI, "ROCA_GROK_API_KEY"},
	} {
		base := settings(t, "[models]\norder = [\""+tc.name+"\"]\n")
		base.Env = func(key string) string {
			if key == tc.alias {
				return "sk-" + tc.alias
			}
			return ""
		}
		cascade, err := BuildCascade(base)
		if err != nil {
			t.Fatalf("%s: build: %v", tc.name, err)
		}
		readiness := cascade.Providers[0].Ready(t.Context())
		if strings.Contains(readiness.Reason, "no credential") {
			t.Errorf("%s: the alias %s was not read: %+v", tc.name, tc.alias, readiness)
		}
	}
}

func TestAnOperatorsOwnEnvironmentVariableIsHonoured(t *testing.T) {
	base := settings(t, `
[models]
order = ["mycorp"]

[models.mycorp]
base_url = "https://llm.internal/v1"
model = "mycorp-7b"
api_key_env = "MYCORP_TOKEN"
`)
	base.Env = func(key string) string {
		if key == "MYCORP_TOKEN" {
			return "sk-mycorp"
		}
		return ""
	}
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	readiness := cascade.Providers[0].Ready(t.Context())
	if strings.Contains(readiness.Reason, "no credential") {
		t.Fatalf("it did not read the declared variable: %+v", readiness)
	}
}

// The environment is the operator asking out loud, right now: it wins over the
// file.
func TestTheEnvironmentOrderWinsOverTheFile(t *testing.T) {
	base := settings(t, "[models]\norder = [\"deepseek\"]\n")
	base.Env = func(key string) string {
		if key == EnvOrder {
			return "ollama"
		}
		return ""
	}
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "ollama" {
		t.Fatalf("order %q", got)
	}
}

// An order named in the environment is a contract: naming what does not exist
// has to be found out at once.
func TestAnUnknownProviderInTheEnvironmentFails(t *testing.T) {
	base := settings(t, "")
	base.Env = func(key string) string {
		if key == EnvOrder {
			return "telepathy"
		}
		return ""
	}
	if _, err := BuildCascade(base); err == nil {
		t.Fatal("an environment order that names what does not exist has to fail")
	}
}

// An operator who writes an empty order means it, and that is not the same as
// writing no order at all, which is what gets the default.
func TestAnEmptyOrderInTheFileLeavesNoProvider(t *testing.T) {
	cascade, err := BuildCascade(settings(t, "[models]\norder = []\n"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(cascade.Providers) != 0 {
		t.Fatalf("providers %v: an empty order is an order, not a missing one",
			names(cascade.Providers))
	}
}

func TestTheModelCanBeTurnedOff(t *testing.T) {
	base := settings(t, "")
	base.Env = func(key string) string {
		if key == EnvOrder {
			return "none"
		}
		return ""
	}
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(cascade.Providers) != 0 || !cascade.Disabled {
		t.Fatalf("providers %v disabled %v", names(cascade.Providers), cascade.Disabled)
	}
}

func TestTheBudgetsComeFromTheConfiguration(t *testing.T) {
	cascade, err := BuildCascade(settings(t, "[models]\ntimeout_ms = 4000\nprobe_ms = 250\n"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cascade.Timeout != 4*time.Second {
		t.Fatalf("timeout %v", cascade.Timeout)
	}
	if cascade.Probe != 250*time.Millisecond {
		t.Fatalf("probe %v", cascade.Probe)
	}
}

func TestAModelKeyUnderDefaultsRetunesTheLocalFloor(t *testing.T) {
	cascade, err := BuildCascade(settings(t,
		"[defaults]\nollama_model = \"qwen3.5:2b\"\n\n[models]\norder = [\"ollama\"]\n"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := cascade.Providers[0].ModelID(); got != "qwen3.5:2b" {
		t.Fatalf("model %q", got)
	}
}

func TestTheConfigWarningsTravelWithTheCascade(t *testing.T) {
	for _, tc := range []struct {
		name, body, warning string
	}{
		{name: "models key", body: "[models]\norder = [\"ollama\"]\nturbo_mode = true\n", warning: "turbo_mode"},
		{name: "HTTP provider key", body: "[models]\norder = [\"deepseek\"]\n[models.deepseek]\nbase_urll = \"https://private.invalid/v1\"\n", warning: "models.deepseek.base_urll"},
		{name: "command scalar", body: "[models]\norder = [\"fixture\"]\n[models.fixture]\ncommand = [\"fixture\", \"{tuning}\"]\nmodel = \"fixture-model\"\ntuning = \"high\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := settings(t, tc.body)
			base.RunnerDir = t.TempDir()
			cascade, err := BuildCascade(base)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			joined := strings.Join(cascade.Warnings, " ")
			if tc.warning != "" && !strings.Contains(joined, tc.warning) {
				t.Fatalf("warning does not contain %q: %v", tc.warning, cascade.Warnings)
			}
			if tc.warning == "" && joined != "" {
				t.Fatalf("command scalar was reported as unknown: %v", cascade.Warnings)
			}
		})
	}
}

func TestCodexTakesItsSessionFromTheCredentialsDirectory(t *testing.T) {
	base := settings(t, "[models]\norder = [\"codex\"]\n\n[models.codex]\nbase_url = \"https://chatgpt.com/backend-api/codex\"\n")
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	readiness := cascade.Providers[0].Ready(t.Context())
	if readiness.Ready {
		t.Fatal("there is no session and it says it is ready")
	}
	if !strings.Contains(readiness.Action, "roca login codex") {
		t.Fatalf("action %q", readiness.Action)
	}
	if got := CodexStore(base.Credentials).Path; got != filepath.Join(base.Credentials, "codex.json") {
		t.Fatalf("the session does not live in the credentials directory: %q", got)
	}
}

func TestTheResolvedCodexModelFollowsConfigOverTheBuiltInDefault(t *testing.T) {
	base := settings(t, "[models]\norder = [\"codex\"]\n\n[models.codex]\nmodel = \"gpt-operator\"\n")
	cascade, err := BuildCascade(base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := cascade.Providers[0].ModelID(); got != "gpt-operator" || got == DefaultCodexModel {
		t.Fatalf("resolved model %q, built-in default %q", got, DefaultCodexModel)
	}
}

// The interpretation order is the second inference's own cascade. Declaring it
// splits the two inferences; leaving it out is an installation where whoever
// wrote the SQL also reads the rows, which is every installation that never
// heard of the key.
func TestTheInterpretationOrderIsACascadeOfItsOwn(t *testing.T) {
	for _, tc := range []struct{ name, body, want, warning string }{
		{name: "absent leaves the two inferences together",
			body: "[models]\norder = [\"ollama\"]\n"},
		{name: "declared names the provider that reads the rows",
			body: "[models]\norder = [\"codex\"]\ninterpret_order = [\"ollama\"]\ntimeout_ms = 15000\n",
			want: "ollama"},
		{name: "a local binary can read the rows",
			body: "[models]\norder = [\"ollama\"]\ninterpret_order = [\"claude\"]\ntimeout_ms = 15000\n",
			want: "claude"},
		{name: "an unknown name warns and leaves no split",
			body:    "[models]\ninterpret_order = [\"telepathy\"]\n",
			warning: "telepathy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cascade, err := BuildInterpretCascade(settings(t, tc.body))
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if got := strings.Join(names(cascade.Providers), ","); got != tc.want {
				t.Fatalf("interpretation order %q, want %q", got, tc.want)
			}
			joined := strings.Join(cascade.Warnings, " ")
			switch {
			case tc.warning == "" && joined != "":
				t.Fatalf("unexpected warnings: %v", cascade.Warnings)
			case tc.warning != "" && !strings.Contains(joined, tc.warning):
				t.Fatalf("the warning does not name %q: %v", tc.warning, cascade.Warnings)
			case tc.warning != "" && !strings.Contains(joined, "models.interpret_order"):
				t.Fatalf("the warning does not name the key: %v", cascade.Warnings)
			}
			if tc.want != "" && cascade.Timeout != 15*time.Second {
				t.Fatalf("the model budget did not reach it: %v", cascade.Timeout)
			}
		})
	}
}
