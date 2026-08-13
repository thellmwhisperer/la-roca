package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestInitAndDoctorNarrateTheModelSourceAndExactChangePaths(t *testing.T) {
	t.Setenv("ROCA_CODEX_MODEL", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.SetProviderModel(path, "codex", "gpt-operator"); err != nil {
		t.Fatal(err)
	}
	for name, render := range map[string]func(*cliEnv){
		"init": func(env *cliEnv) {
			renderBootstrap(env, service.InitResult{ConfigPath: path, Model: &service.InitModel{
				Ready: true, Provider: "codex", Model: "gpt-operator",
			}})
		},
		"doctor": func(env *cliEnv) {
			renderDoctor(env, service.DoctorReport{ConfigPath: path, Providers: []service.DoctorProvider{{
				Name: "codex", Ready: true, Model: "gpt-operator",
			}}, Titular: "codex"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			var output strings.Builder
			render(&cliEnv{out: &output})
			for _, want := range []string{
				"gpt-operator", "from " + path, "models.codex.model",
				"roca model set <id>",
			} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("output does not carry %q:\n%s", want, output.String())
				}
			}
		})
	}
}

// A positive-only test on "built-in default" is a placebo: it stays green when
// the built-in label leaks into a config-file or env-var source line, and it
// says nothing about whether the config line is shown when it SHOULD be.
func TestBuiltInModelSourceIsVisible(t *testing.T) {
	t.Setenv("ROCA_CODEX_MODEL", "")

	// Positive: without a config file or env var, the source is built-in.
	t.Run("built-in when nothing else configured", func(t *testing.T) {
		var output strings.Builder
		renderBootstrap(&cliEnv{out: &output}, service.InitResult{Model: &service.InitModel{
			Ready: true, Provider: "codex", Model: "gpt-5.6-luna",
		}})
		if !strings.Contains(output.String(), "built-in default") {
			t.Fatalf("built-in source is invisible:\n%s", output.String())
		}
	})

	// Negative: with a config file that sets the model, "built-in default"
	// must NOT appear — the source is the config file.
	t.Run("config source is not built-in", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := config.SetProviderModel(path, "codex", "gpt-custom"); err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		renderBootstrap(&cliEnv{out: &output}, service.InitResult{
			ConfigPath: path,
			Model: &service.InitModel{
				Ready: true, Provider: "codex", Model: "gpt-custom",
			},
		})
		if strings.Contains(output.String(), "built-in default") {
			t.Fatalf("source should not say built-in when a config file sets it:\n%s", output.String())
		}
		if !strings.Contains(output.String(), "from "+path) {
			t.Fatalf("config source should name the config path:\n%s", output.String())
		}
	})

	// An environment variable is the source only where the provider reads it.
	// Ollama does; a shipped CLI preset resolves its model from the file alone,
	// so a variable that merely matches the answer is not what chose it. The
	// rendered line carries exactly one source, so naming the expected one also
	// pins that the other spelling is absent.
	for _, source := range []struct{ name, key, provider, want string }{
		{"ollama reads its environment override", "ROCA_OLLAMA_MODEL", "ollama", "from ROCA_OLLAMA_MODEL"},
		{"codex ignores an environment override", "ROCA_CODEX_MODEL", "codex", "built-in default"},
	} {
		t.Run(source.name, func(t *testing.T) {
			t.Setenv(source.key, "env-model")
			var output strings.Builder
			renderBootstrap(&cliEnv{out: &output}, service.InitResult{Model: &service.InitModel{
				Ready: true, Provider: source.provider, Model: "env-model",
			}})
			if !strings.Contains(output.String(), source.want) {
				t.Fatalf("model source should be %q:\n%s", source.want, output.String())
			}
		})
	}
}

func TestInitSaysDetectedLocalCLIIsReadyWithoutRocaLogin(t *testing.T) {
	var output strings.Builder
	renderBootstrap(&cliEnv{out: &output}, service.InitResult{
		DetectedModelBinaries: []string{"claude", "codex"}, FactoryDefault: true,
		FactoryDefaultProvider: "claude",
		Model: &service.InitModel{
			Ready: true, Provider: "claude", Model: "factory-model", CommandTransport: true,
		},
	})
	for _, want := range []string{
		"model binaries detected: claude, codex", "factory default selected: claude",
		"confirm it with roca model check", "uses the existing local CLI session", "roca query",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("init output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestDoctorExactProviderProbeNarration(t *testing.T) {
	t.Setenv("ROCA_CODEX_MODEL", "")
	var output strings.Builder
	renderDoctor(&cliEnv{out: &output}, service.DoctorReport{
		Version: "test", SourceSHA: "sha", DBPath: "/data/roca.db", ConfigPath: "/data/config.toml",
		Providers: []service.DoctorProvider{{Name: "codex", Model: "gpt-test",
			Reason: "codex binary not found in PATH", Action: "install Codex CLI"}},
	})
	wantLine := "  [no] codex · model gpt-test (built-in default · change with: roca model set <id> or models.codex.model in /data/config.toml) · probe failed\n" +
		"      codex binary not found in PATH\n      remedy: install Codex CLI\n"
	if !strings.Contains(output.String(), wantLine) {
		t.Fatalf("doctor provider block changed:\n--- want block ---\n%s--- got ---\n%s", wantLine, output.String())
	}
}
