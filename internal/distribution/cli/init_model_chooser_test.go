package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestTTYInitListsDetectedModelsAndEnterKeepsTheFactoryDefault(t *testing.T) {
	home, bin := initChooserHome(t)
	fakeModelCLI(t, bin, provider.NameClaude)
	fakeModelCLI(t, bin, provider.NameCodex)

	out, err := runInitChooser(t, true, "new\n\n\n", chooserTestBackend{
		catalogues: map[string]modelCatalogue{
			provider.NameOllama: {IDs: []string{"local-one"}},
		},
	}, "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Which model do you want answering?",
		"claude (detected CLI)", "sonnet",
		"codex (detected CLI)", provider.DefaultCodexModel,
		"ollama (locally pulled)", "local-one",
		"Harness: claude (only detected harness for sonnet)",
		"Use claude/sonnet? [Y/n]:",
		"configuration updated: " + filepath.Join(home, ".roca", "config.toml"),
		"answering: claude/sonnet",
		"configuration: " + filepath.Join(home, ".roca", "config.toml"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init chooser does not contain %q:\n%s", want, out)
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, ".roca", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	for _, want := range []string{
		`order = ["claude", "codex", "ollama"]`,
		"[models.claude]",
		`model = "sonnet"`,
		"[features]",
		"plugins = true",
		"roca_ops = true",
		"cron = true",
		"vector = false",
	} {
		if !strings.Contains(config, want) {
			t.Errorf("config does not contain %q:\n%s", want, config)
		}
	}
	if strings.Contains(out, "Which harness") {
		t.Fatalf("a model with one exact harness asked a redundant question:\n%s", out)
	}
}

func TestTTYAdoptChoosesALocallyPulledModelModelFirst(t *testing.T) {
	home, bin := initChooserHome(t)
	fakeModelCLI(t, bin, provider.NameClaude)
	source := seedCandidate(t, filepath.Join(home, "import", "candidate.db"))

	out, err := runInitChooser(t, true, "adopt\n"+source+"\nlocal-one\n\n", chooserTestBackend{
		catalogues: map[string]modelCatalogue{
			provider.NameOllama: {IDs: []string{"local-one"}},
		},
	}, "init")
	if err != nil {
		t.Fatalf("adopt init: %v\n%s", err, out)
	}
	for _, want := range []string{
		"adopted by copy", "Which model do you want answering?",
		"Harness: ollama (only detected harness for local-one)",
		"answering: ollama/local-one",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adopt chooser does not contain %q:\n%s", want, out)
		}
	}
}

func TestTTYFreeTextModelAsksWhichDetectedHarnessServesIt(t *testing.T) {
	_, bin := initChooserHome(t)
	fakeModelCLI(t, bin, provider.NameClaude)
	fakeModelCLI(t, bin, provider.NameCodex)

	out, err := runInitChooser(t, true, "new\nprivate-model\ncodex\n\n", chooserTestBackend{}, "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Which harness serves private-model?",
		"claude", "codex",
		"Use codex/private-model? [Y/n]:",
		"answering: codex/private-model",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("free-text chooser does not contain %q:\n%s", want, out)
		}
	}
}

func TestTTYInitWritesSurgicallyWithBackupAndNamesIt(t *testing.T) {
	home, bin := initChooserHome(t)
	fakeModelCLI(t, bin, provider.NameClaude)
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "# operator note\n[models]\nprobe_ms = 500\norder = [\"ollama\"]\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, ".roca", "roca.db")

	out, err := runInitChooser(t, true, "sonnet\n\n", chooserTestBackend{},
		"init", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	backup := path + ".roca.bak"
	if !strings.Contains(out, "backup: "+backup) {
		t.Fatalf("init did not name its config backup:\n%s", out)
	}
	backupRaw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(backupRaw) != before {
		t.Fatalf("backup changed:\n--- want ---\n%s--- got ---\n%s", before, backupRaw)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# operator note") ||
		!strings.Contains(string(raw), "probe_ms = 500") ||
		!strings.Contains(string(raw), `order = ["claude", "ollama"]`) ||
		!strings.Contains(string(raw), `[models.claude]`) {
		t.Fatalf("surgical config edit lost operator content:\n%s", raw)
	}
}

// The model chooser persists a pair. It is not the retirement prompt: it writes
// a secret-free recovery backup of its own, and it leaves every legacy setting
// and every credential file an older release left behind exactly where they are,
// because removing those is what the visible accept/decline proposal is for.
func TestInitModelChoiceWritesTheChoiceAndRetiresNothing(t *testing.T) {
	tests := []struct {
		name, body       string
		legacyCredential bool
		preserved        []string
	}{
		{
			name:             "leftover credential file",
			body:             "[models]\norder = [\"codex\"]\n\n[models.codex]\nmodel = \"gpt-legacy\"\n",
			legacyCredential: true,
		},
		{
			name:      "quoted inline legacy key",
			body:      "[models]\norder = [\"codex\"]\n\n[models.codex]\n\"api_key\" = \"legacy-secret\"\nmodel = \"gpt-legacy\"\n",
			preserved: []string{"api_key"},
		},
		{
			name:      "unrelated provider secret",
			body:      "[models]\norder = [\"xai\"]\n\n[models.xai]\napi_key = \"unrelated-secret\"\nmodel = \"grok-legacy\"\n",
			preserved: []string{"[models.xai]", "api_key"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths, credential := initRetirementFixture(t, test.body, test.legacyCredential)
			if _, err := writeInitModelChoice(paths, provider.NameCodex, "gpt-current"); err != nil {
				t.Fatal(err)
			}

			backup, err := os.ReadFile(paths.Config + ".roca.bak")
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"legacy-secret", "unrelated-secret"} {
				if strings.Contains(string(backup), secret) {
					t.Fatalf("provider secret %q survived in the recovery backup:\n%s", secret, backup)
				}
			}
			raw, err := os.ReadFile(paths.Config)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), `model = "gpt-current"`) {
				t.Fatalf("the confirmed model was not persisted:\n%s", raw)
			}
			for _, kept := range test.preserved {
				if !strings.Contains(string(raw), kept) {
					t.Fatalf("the model choice deleted the legacy setting %q:\n%s", kept, raw)
				}
			}
			if credential != "" {
				if _, err := os.Stat(credential); err != nil {
					t.Fatalf("the model choice removed a legacy credential file: %v", err)
				}
			}
		})
	}
}

func initRetirementFixture(t *testing.T, body string, legacyCredential bool) (config.Paths, string) {
	t.Helper()
	root := t.TempDir()
	paths := config.Paths{DB: filepath.Join(root, "roca.db"), Config: filepath.Join(root, "config.toml")}
	if err := os.WriteFile(paths.Config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !legacyCredential {
		return paths, ""
	}
	credential := legacyProviderCredentialPaths(root)[provider.NameCodex]
	writeFile(t, credential, "legacy-file-secret")
	return paths, credential
}

func TestTTYInitReportsTheEffectiveModelAfterPersistence(t *testing.T) {
	tests := []struct {
		name          string
		prepare       func(*testing.T, string, string)
		input         string
		backend       chooserTestBackend
		want          string
		avoid         string
		guidance      []string
		avoidGuidance string
		orderEnv      string
		modelEnv      bool
	}{
		{
			name: "environment order",
			prepare: func(t *testing.T, _, bin string) {
				fakeModelCLI(t, bin, provider.NameClaude)
				fakeModelCLI(t, bin, provider.NameCodex)
				t.Setenv("ROCA_MODELS_ORDER", provider.NameCodex)
			},
			input:         "sonnet\n\n",
			want:          "answering: codex/" + provider.DefaultCodexModel,
			avoid:         "answering: claude/sonnet",
			guidance:      []string{"unset ROCA_MODELS_ORDER before using models.<provider>.model"},
			avoidGuidance: "which makes",
		},
		{
			name:  "provider model environment",
			input: "local-one\n\n",
			backend: chooserTestBackend{catalogues: map[string]modelCatalogue{
				provider.NameOllama: {IDs: []string{"local-one"}},
			}},
			want:     "answering: ollama/environment-model",
			avoid:    "answering: ollama/local-one",
			guidance: []string{"change ROCA_OLLAMA_MODEL directly; or unset ROCA_OLLAMA_MODEL and ROCA_MODEL before using roca model set"},
			modelEnv: true,
		},
		{
			name:  "stacked order and model environment",
			input: "local-one\n\n",
			backend: chooserTestBackend{catalogues: map[string]modelCatalogue{
				provider.NameOllama: {IDs: []string{"local-one"}},
			}},
			want:          "answering: ollama/environment-model",
			avoid:         "answering: ollama/local-one",
			guidance:      []string{"unset ROCA_MODELS_ORDER and ROCA_OLLAMA_MODEL and ROCA_MODEL before using models.<provider>.model"},
			avoidGuidance: "which makes",
			orderEnv:      provider.NameOllama,
			modelEnv:      true,
		},
		{
			name: "retired base URL is retired by its own visible proposal",
			prepare: func(t *testing.T, home, bin string) {
				fakeModelCLI(t, bin, provider.NameClaude)
				writeConfig(t, home, "[models]\norder = [\"ollama\"]\n\n[models.claude]\nbase_url = \"https://example.invalid/v1\"\napi_key = \"synthetic-key\"\nmodel = \"remote-old\"\n")
			},
			input: "sonnet\n\ny\n",
			want:  "answering: claude/sonnet",
			guidance: []string{"Remove the retired claude authentication settings?",
				"uses the existing local CLI session"},
			avoidGuidance: "transport is governed by models.claude.base_url",
		},
		{
			name: "persisted custom command",
			prepare: func(t *testing.T, home, bin string) {
				fakeModelCLI(t, bin, provider.NameClaude)
				writeConfig(t, home, "[models]\norder = [\"ollama\"]\n\n[models.claude]\ncommand = [\"missing-custom-claude\", \"{prompt}\"]\nmodel = \"custom-old\"\n\n[models.ollama]\nmodel = \"local-fallback\"\n")
			},
			input: "sonnet\n\n",
			want:  "answering: ollama/local-fallback",
			avoid: "answering: claude/sonnet",
		},
		{
			name: "ready persisted custom command",
			prepare: func(t *testing.T, home, bin string) {
				fakeModelCLI(t, bin, provider.NameClaude)
				if err := os.WriteFile(filepath.Join(bin, "custom-claude"),
					[]byte("#!/bin/sh\nprintf 'SELECT 1\\n'\n"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeConfig(t, home,
					"[models]\norder = [\"ollama\"]\n\n[models.claude]\ncommand = [\"custom-claude\", \"{prompt}\"]\nmodel = \"custom-old\"\n")
			},
			input:    "sonnet\n\n",
			want:     "answering: claude/sonnet",
			guidance: []string{"transport is governed by models.claude.command"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home, bin := initChooserHome(t)
			if test.prepare != nil {
				test.prepare(t, home, bin)
			}
			if test.orderEnv != "" {
				t.Setenv("ROCA_MODELS_ORDER", test.orderEnv)
			}
			if test.modelEnv {
				t.Setenv("ROCA_OLLAMA_MODEL", "environment-model")
				t.Setenv("ROCA_MODEL", "local-fallback")
			}
			out, err := runInitChooser(t, true, test.input, test.backend,
				"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
			if err != nil {
				t.Fatalf("init: %v\n%s", err, out)
			}
			if !strings.Contains(out, test.want) || test.avoid != "" && strings.Contains(out, test.avoid) {
				t.Fatalf("effective choice mismatch: want %q and avoid %q:\n%s", test.want, test.avoid, out)
			}
			for _, guidance := range test.guidance {
				if !strings.Contains(out, guidance) {
					t.Fatalf("effective guidance does not contain %q:\n%s", guidance, out)
				}
			}
			if test.avoidGuidance != "" && strings.Contains(out, test.avoidGuidance) {
				t.Fatalf("effective guidance still contains %q:\n%s", test.avoidGuidance, out)
			}
			if !strings.HasSuffix(strings.TrimSpace(out), "run roca doctor to confirm who will answer") {
				t.Fatalf("effective guidance does not end with the verification instruction:\n%s", out)
			}
		})
	}
}

func TestReinitializeChooserFailureLeavesTheDatabaseUntouched(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "invalid harness", input: "reinitialize\nprivate-model\ninvalid\n", wantErr: true},
		{name: "canceled confirmation", input: "reinitialize\nsonnet\nn\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home, bin := initChooserHome(t)
			fakeModelCLI(t, bin, provider.NameClaude)
			fakeModelCLI(t, bin, provider.NameCodex)
			path := seedCandidate(t, filepath.Join(home, ".roca", "roca.db"))
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out, runErr := runInitChooser(t, true, test.input, chooserTestBackend{}, "init")
			if (runErr != nil) != test.wantErr {
				t.Fatalf("init error=%v, want error=%v:\n%s", runErr, test.wantErr, out)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("reinitialize changed the database after chooser failure:\n%s", out)
			}
			if !test.wantErr {
				last := strings.TrimSpace(out)
				last = last[strings.LastIndex(last, "\n")+1:]
				if !strings.HasPrefix(last, "answering: claude/sonnet") ||
					!strings.Contains(last, "configuration: "+filepath.Join(home, ".roca", "config.toml")) {
					t.Fatalf("canceled reinitialize did not end with the unchanged answer:\n%s", out)
				}
			}
		})
	}
}

func TestNonTTYInitPrintsOneAnsweringAlertAndWritesNewInstallConfig(t *testing.T) {
	home, bin := initChooserHome(t)
	fakeModelCLI(t, bin, provider.NameClaude)
	dbPath := filepath.Join(home, "explicit", "roca.db")
	configPath := filepath.Join(filepath.Dir(dbPath), "config.toml")

	out, err := runInitChooser(t, false, "", chooserTestBackend{},
		"init", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if count := countLinesWithPrefix(out, "answering:"); count != 1 {
		t.Fatalf("answering alert count=%d, want 1:\n%s", count, out)
	}
	for _, want := range []string{
		"answering: claude/sonnet",
		"roca model set <id>",
		"models.claude.model in " + configPath,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("non-TTY alert does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Which model") {
		t.Fatalf("non-TTY init prompted:\n%s", out)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read new-install config: %v", err)
	}
	want := "[features]\nplugins = true\nroca_ops = true\ncron = true\nvector = false\n"
	if string(raw) != want {
		t.Fatalf("new-install config:\n--- want ---\n%s--- got ---\n%s", want, raw)
	}
}

type chooserTestBackend struct {
	catalogues map[string]modelCatalogue
}

func (b chooserTestBackend) Catalogue(_ context.Context, name, _ string) (modelCatalogue, error) {
	if catalogue, ok := b.catalogues[name]; ok {
		return catalogue, nil
	}
	return modelCatalogue{}, fmt.Errorf("no enumerable catalogue")
}

func (chooserTestBackend) Probe(context.Context, string, string) error { return nil }

func initChooserHome(t *testing.T) (string, string) {
	t.Helper()
	home, bin := t.TempDir(), t.TempDir()
	isolateRuntimeDirs(t, home)
	t.Setenv("PATH", bin)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "")
	t.Setenv("ROCA_CODEX_MODEL", "")
	t.Setenv("ROCA_OLLAMA_MODEL", "")
	t.Setenv("ROCA_MODEL", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"local-one"},{"name":"environment-model"},{"name":"local-fallback"}]}`))
		case "/api/chat":
			_, _ = w.Write([]byte(`{"message":{"content":"SELECT 1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROCA_OLLAMA_BASE_URL", server.URL)
	return home, bin
}

func fakeModelCLI(t *testing.T, bin, name string) {
	t.Helper()
	body := "#!/bin/sh\nprintf 'SELECT 1\\n'\n"
	if name == provider.NameClaude {
		body = "#!/bin/sh\nprintf '{\"result\":\"SELECT 1\"}\\n'\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runInitChooser(t *testing.T, tty bool, input string, backend modelValidationBackend,
	args ...string) (string, error) {
	t.Helper()
	previous := terminalInput
	terminalInput = func(any) bool { return tty }
	t.Cleanup(func() { terminalInput = previous })
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: Build{Version: "test", Commit: "test-sha"}, out: &out, errOut: &out,
	})
	env.skipInitChooser = false
	env.skipReconciliation = false
	env.modelBackend = backend
	_, err := executeWithEnv(env, args, strings.NewReader(input))
	return out.String(), err
}

func countLinesWithPrefix(text, prefix string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}
