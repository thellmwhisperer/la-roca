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
	path := filepath.Join(t.TempDir(), "config.toml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return Settings{File: file, RunnerDir: t.TempDir(), Env: func(string) string { return "" }}
}

func lookPath(names ...string) LookPathFunc {
	return func(name string) (string, error) {
		for _, candidate := range names {
			if candidate == name {
				return "/synthetic/" + name, nil
			}
		}
		return "", os.ErrNotExist
	}
}

func TestFactoryOrderUsesDetectedCLIsThenOllama(t *testing.T) {
	for _, tc := range []struct {
		name     string
		detected []string
		want     string
	}{
		{name: "none", want: "ollama"},
		{name: "codex", detected: []string{"codex"}, want: "codex,ollama"},
		{name: "both", detected: []string{"claude", "codex"}, want: "claude,codex,ollama"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := settings(t, "")
			s.LookPath = lookPath(tc.detected...)
			cascade, err := BuildCascade(s)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(names(cascade.Providers), ","); got != tc.want || !cascade.FactoryDefault {
				t.Fatalf("order %q factory=%v, want %q", got, cascade.FactoryDefault, tc.want)
			}
		})
	}
}

func TestConfiguredOrderWinsAndUnknownProvidersDegrade(t *testing.T) {
	s := settings(t, "[models]\norder = [\"retired\", \"ollama\", \"codex\"]\n")
	s.LookPath = lookPath("codex")
	cascade, err := BuildCascade(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(cascade.Providers), ","); got != "ollama,codex" {
		t.Fatalf("order = %q", got)
	}
	warnings := strings.Join(cascade.Warnings, "\n")
	if !strings.Contains(warnings, `provider "retired"`) || !strings.Contains(warnings, "Available providers: claude, codex, ollama") {
		t.Fatalf("warnings = %v", cascade.Warnings)
	}
}

func TestRetiredCredentialConfigurationsNeverCrashAndDegradeHonestly(t *testing.T) {
	cases := []struct {
		name, body, wantProvider, wantWarning string
	}{
		{
			name:        "OAuth-backed Codex",
			body:        "[models]\norder = [\"codex\"]\n[models.codex]\nbase_url = \"https://chatgpt.com/backend-api/codex\"\napi_key = \"legacy-secret\"\n",
			wantWarning: "is ignored; accept or decline the migration proposal",
		},
		{
			name: "API-key provider", wantProvider: NameOllama,
			body:        "[models]\norder = [\"xai\", \"ollama\"]\n[models.xai]\napi_key = \"legacy-secret\"\n",
			wantWarning: `provider "xai"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			s := settings(t, tc.body)
			s.LookPath = lookPath("codex")
			cascade, err := BuildCascade(s)
			if err != nil {
				t.Fatalf("legacy config broke the cascade: %v", err)
			}
			if tc.wantProvider == "" && len(cascade.Providers) != 0 {
				t.Fatalf("providers = %+v, want honest degradation until migration", cascade.Providers)
			}
			if tc.wantProvider != "" && (len(cascade.Providers) != 1 || cascade.Providers[0].Name() != tc.wantProvider) {
				t.Fatalf("providers = %+v, want only %s", cascade.Providers, tc.wantProvider)
			}
			if !strings.Contains(strings.Join(cascade.Warnings, "\n"), tc.wantWarning) {
				t.Fatalf("warnings = %v, want %q", cascade.Warnings, tc.wantWarning)
			}
		})
	}
}

func TestCustomCommandProviderRemainsSupported(t *testing.T) {
	s := settings(t, "[models]\norder = [\"fixture\"]\n[models.fixture]\ncommand = [\"fixture-agent\", \"{prompt}\"]\nmodel = \"fixture-model\"\n")
	cascade, err := BuildCascade(s)
	if err != nil || len(cascade.Providers) != 1 {
		t.Fatalf("cascade=%+v err=%v", cascade, err)
	}
	if got := cascade.Providers[0]; got.Name() != "fixture" || got.ModelID() != "fixture-model" {
		t.Fatalf("provider = %s/%s", got.Name(), got.ModelID())
	}
}

func TestEnvironmentOrderIsAnImmediateContract(t *testing.T) {
	s := settings(t, "[models]\norder = [\"ollama\"]\n")
	s.Env = func(key string) string {
		if key == EnvOrder {
			return "codex,ollama"
		}
		return ""
	}
	cascade, err := BuildCascade(s)
	if err != nil || strings.Join(names(cascade.Providers), ",") != "codex,ollama" {
		t.Fatalf("cascade=%+v err=%v", cascade, err)
	}
	s.Env = func(string) string { return "retired" }
	if _, err := BuildCascade(s); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Fatalf("unknown environment provider err = %v", err)
	}
}

func TestDisabledEmptyAndBudgetedOrders(t *testing.T) {
	for _, tc := range []struct {
		body      string
		providers int
		disabled  bool
	}{
		{body: "[models]\norder = []\n"},
		{body: "[models]\norder = [\"off\"]\n", disabled: true},
	} {
		cascade, err := BuildCascade(settings(t, tc.body))
		if err != nil || len(cascade.Providers) != tc.providers || cascade.Disabled != tc.disabled {
			t.Fatalf("cascade=%+v err=%v", cascade, err)
		}
	}
	cascade, err := BuildCascade(settings(t, "[models]\norder = [\"ollama\"]\ntimeout_ms = 1250\nprobe_ms = 75\n"))
	if err != nil || cascade.Timeout != 1250*time.Millisecond || cascade.Probe != 75*time.Millisecond {
		t.Fatalf("budgets = %s/%s err=%v", cascade.Timeout, cascade.Probe, err)
	}
}

func TestModelsAndInterpretationOrderUseOnlySupportedTransports(t *testing.T) {
	s := settings(t, "[models]\norder = [\"codex\"]\ninterpret_order = [\"retired\", \"ollama\"]\n[models.codex]\nmodel = \"gpt-configured\"\n")
	s.LookPath = lookPath("codex")
	main, err := BuildCascade(s)
	if err != nil || main.Providers[0].ModelID() != "gpt-configured" {
		t.Fatalf("main=%+v err=%v", main, err)
	}
	interpreters, err := BuildInterpretCascade(s)
	if err != nil || strings.Join(names(interpreters.Providers), ",") != "ollama" ||
		!strings.Contains(strings.Join(interpreters.Warnings, "\n"), "retired") {
		t.Fatalf("interpreters=%+v err=%v", interpreters, err)
	}
}
