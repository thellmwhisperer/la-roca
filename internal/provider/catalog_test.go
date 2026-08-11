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

func TestWithNoConfigTheOrderIsTheDefaultOne(t *testing.T) {
	cascade, err := BuildCascade(settings(t, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "codex,ollama" {
		t.Fatalf("order %q", got)
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
	cascade, err := BuildCascade(settings(t, "[models]\norder = [\"ollama\"]\nturbo_mode = true\n"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(strings.Join(cascade.Warnings, " "), "turbo_mode") {
		t.Fatalf("the config warning got lost: %v", cascade.Warnings)
	}
}

func TestCodexTakesItsSessionFromTheCredentialsDirectory(t *testing.T) {
	base := settings(t, "[models]\norder = [\"codex\"]\n")
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
