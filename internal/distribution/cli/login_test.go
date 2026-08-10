package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/oauth"
)

// A bare `roca login` lists what this build supports instead of a Cobra arity
// error. Same verb for every provider: subscription and key flow, one line each.
func TestBareLoginExactOutputWithoutSessions(t *testing.T) {
	home := isolatedLoginHome(t)
	configPath := filepath.Join(home, ".roca", "config.toml")
	out := runRoot(t, Build{Version: "test"}, "login")
	want := "Supported providers:\n" +
		"  codex       subscription  ·  roca login codex\n" +
		"  deepseek    API key       ·  roca login deepseek\n" +
		"  zai         API key       ·  roca login zai\n" +
		"  xai         API key       ·  roca login xai\n" +
		"Model configuration:\n" +
		"  order: codex, ollama (built-in default · change with: models.order in " + configPath + ")\n" +
		"  codex: model gpt-5.6-luna (built-in default · change with: roca login codex --model <id> or models.codex.model in " + configPath + ")\n" +
		"  ollama: model qwen3.5:4b (built-in default · change with: models.ollama.model in " + configPath + ")\n" +
		"Credential and session state:\n" +
		"  codex: no session\n" +
		"  deepseek: no stored API key\n" +
		"  zai: no stored API key\n" +
		"  xai: no stored API key\n" +
		"  ollama: local runtime, no credential needed"
	if out != want {
		t.Fatalf("bare login output changed:\n--- want ---\n%s\n--- got ---\n%s", want, out)
	}
}

func TestBareLoginExactOutputWithASession(t *testing.T) {
	home := isolatedLoginHome(t)
	expires := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	store := provider.CodexStore(filepath.Join(home, ".roca", "credentials"))
	if err := store.Save(oauth.Token{AccessToken: "secret", ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(
		bareLoginWithoutSession(t, home),
		"  codex: no session", "  codex: session present (expires 2026-08-17)", 1)
	if out := runRoot(t, Build{Version: "test"}, "login"); out != want {
		t.Fatalf("session listing changed:\n--- want ---\n%s\n--- got ---\n%s", want, out)
	}
}

// withTempHome sets up a temp HOME and returns its path.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// An unknown name fails with an error that names what was asked for and then
// prints the full catalogue. Both parts are needed: without this structure a
// single stray word in the output could mask the error.
func TestUnknownLoginProviderListsTheSupportedOnes(t *testing.T) {
	_, err := runRootErr(t, Build{Version: "test"}, nil, "login", "nope")
	if err == nil {
		t.Fatal("an unknown provider has to fail")
	}
	msg := err.Error()

	prefix := `there is no login for "nope"`
	if !strings.HasPrefix(msg, prefix) {
		t.Errorf("error does not name the unknown provider upfront: %v", err)
	}
	if !strings.Contains(msg, loginCatalogue()) {
		t.Errorf("error does not contain the full catalogue:\n%s", msg)
	}
}

// The login help text names both flows in order (subscription → API key)
// without the stray indentation that used to leave "Providers with a
// subscription flow:" floating.
func TestLoginHelpNamesBothFlowsCleanly(t *testing.T) {
	out := runRoot(t, Build{Version: "test"}, "login", "--help")

	if strings.Contains(out, "  Providers with a subscription flow") {
		t.Fatalf("stray indentation is back in the help:\n%s", out)
	}

	subPos := strings.Index(out, "subscription")
	keyPos := strings.Index(out, "API key")
	if subPos < 0 || keyPos < 0 {
		t.Fatalf("help missing a flow name:\n%s", out)
	}
	if subPos >= keyPos {
		t.Errorf("subscription must appear before API key:\n%s", out)
	}

	for _, want := range []string{
		"codex", "xai", "zai", "deepseek",
		"--model", "[models.<provider>]", "config.toml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not carry %q:\n%s", want, out)
		}
	}
}

func TestLoginModelPersistsTheChoiceAndNarratesItsSource(t *testing.T) {
	home := isolatedLoginHome(t)
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "# keep this note\nworkspace_roots = [\"/work\"]\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runRootErr(t, Build{Version: "test"}, strings.NewReader("sk-test\n"),
		"login", "xai", "--model", "grok-chosen")
	if err != nil {
		t.Fatalf("login: %v\nout=%s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantConfig := before + "\n[models]\norder = [\"xai\", \"codex\", \"ollama\"]\n\n[models.xai]\nmodel = \"grok-chosen\"\n"
	if string(raw) != wantConfig {
		t.Fatalf("persisted config changed:\n--- want ---\n%s--- got ---\n%s", wantConfig, raw)
	}
	wantOut := "Paste your xAI (Grok) API key: logged in to xai: the key is at " +
		filepath.Join(home, ".roca", "credentials", "xai.key") + "\n" +
		"model: xai selected (grok-chosen, from " + path + " · change with: roca login xai --model <id> or models.xai.model in " + path + ")\n" +
		"run `roca init` next; xai will be probed before it answers\n" +
		"forget it with `roca logout xai`\n"
	if out != wantOut {
		t.Fatalf("login narration changed:\n--- want ---\n%s--- got ---\n%s", wantOut, out)
	}
}

func TestFailedLoginLeavesNoOrphanCredential(t *testing.T) {
	home := isolatedLoginHome(t)
	configPath := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runRootErr(t, Build{Version: "test"}, strings.NewReader("orphan\n"), "login", "xai")
	if err == nil {
		t.Fatal("malformed configuration must fail login")
	}
	if _, statErr := os.Stat(provider.APIKeyPath(filepath.Join(home, ".roca", "credentials"), "xai")); !os.IsNotExist(statErr) {
		t.Fatalf("failed login left a credential behind: %v", statErr)
	}
}

func TestLoginAndLogoutHonorJSON(t *testing.T) {
	home := isolatedLoginHome(t)
	for name, args := range map[string][]string{
		"bare":    {"--json", "login"},
		"unknown": {"--json", "login", "nope"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			env := &cliEnv{build: Build{Version: "test"}, out: &out, errOut: &out}
			root := rootCommand(env)
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(out.String()), &got); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, out.String())
			}
			if name == "unknown" && (env.code != ExitError || got["error"] != `there is no login for "nope"`) {
				t.Fatalf("unknown result = %#v, code=%d", got, env.code)
			}
			if name == "bare" && (env.code != ExitOK || got["configuration"] == nil || got["providers"] == nil) {
				t.Fatalf("bare result = %#v, code=%d", got, env.code)
			}
		})
	}
	creds := filepath.Join(home, ".roca", "credentials")
	if err := provider.SaveAPIKey(creds, "xai", "secret"); err != nil {
		t.Fatal(err)
	}
	out := runRoot(t, Build{Version: "test"}, "--json", "logout", "xai")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"forgotten": true, "provider": "xai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("logout JSON = %#v, want %#v", got, want)
	}
	var unknown strings.Builder
	env := &cliEnv{build: Build{Version: "test"}, out: &unknown, errOut: &unknown}
	root := rootCommand(env)
	root.SetArgs([]string{"--json", "logout", "nope"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(unknown.String()), &got); err != nil ||
		env.code != ExitError || got["error"] != `there is no credential store for "nope"` {
		t.Fatalf("unknown logout = %#v, code=%d, err=%v", got, env.code, err)
	}
}

// `roca login xai` reads the key from the terminal (or stdin in tests), stores
// it under the credentials directory at 0600, and confirms without echoing it.
func TestKeyLoginStoresTheCredentialAt0600(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const secret = "sk-xai-from-the-prompt"
	out, err := runRootErr(t, Build{Version: "test"}, strings.NewReader(secret+"\n"), "login", "xai")
	if err != nil {
		t.Fatalf("login xai: %v\nout=%s", err, out)
	}

	path := provider.APIKeyPath(filepath.Join(home, ".roca", "credentials"), provider.NameXAI)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the key was not stored at %s: %v", path, err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credential mode is %o, want 600", mode)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != secret {
		t.Fatalf("stored %q, want %q", got, secret)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("the key leaked to the output: %q", out)
	}

	// Confirmation: the exact message, not a bag-of-words pair.
	want := fmt.Sprintf("logged in to xai: the key is at %s", path)
	if !strings.Contains(out, want) {
		t.Fatalf("no structured confirmation; want %q, got %q", want, out)
	}
	if !strings.Contains(out, "xAI") {
		t.Fatalf("prompt did not name xAI: %q", out)
	}
}

// Tests the JSON output contracts for login and logout. Every kernel command
// answers --json (TECH-SPEC 1.8). Field equality, not substring hunts.
func TestLoginAndLogoutJSONContracts(t *testing.T) {
	// Bare login --json: structured provider catalogue.
	t.Run("bare login", func(t *testing.T) {
		out, err := runRootErr(t, Build{Version: "test"}, nil, "login", "--json")
		if err != nil {
			t.Fatalf("bare login --json: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("invalid JSON:\n%s", out)
		}
		providers, ok := result["providers"].([]any)
		if !ok {
			t.Fatalf("providers field missing or not an array")
		}
		all := provider.KeyProviders()
		if len(providers) != 1+len(all) {
			t.Errorf("expected %d providers, got %d", 1+len(all), len(providers))
		}
		first, _ := providers[0].(map[string]any)
		if first["name"] != provider.NameCodex || first["flow"] != "subscription" {
			t.Errorf("codex entry wrong: %v", first)
		}
		for i, name := range all {
			e, _ := providers[1+i].(map[string]any)
			if e["name"] != name || e["flow"] != "api_key" {
				t.Errorf("%s entry wrong: %v", name, e)
			}
		}
	})

	// Login key --json: provider, path, model, model_source fields.
	t.Run("login xai", func(t *testing.T) {
		_ = withTempHome(t)
		out, err := runRootErr(t, Build{Version: "test"}, strings.NewReader("sk-xai-json-test\n"),
			"login", "--json", "xai")
		if err != nil {
			t.Fatalf("login --json: %v", err)
		}
		// readSecret writes prompt to stdout before JSON.
		s := strings.Index(out, "{")
		if s < 0 {
			t.Fatalf("no JSON in:\n%s", out)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out[s:]), &result); err != nil {
			t.Fatalf("invalid JSON:\n%s", out[s:])
		}
		if p, _ := result["provider"].(string); p != "xai" {
			t.Errorf("provider: %q", p)
		}
		for _, f := range []string{"model", "model_source", "path"} {
			if v, _ := result[f].(string); v == "" {
				t.Errorf("%s missing or empty", f)
			}
		}
	})

	// Logout --json: provider and forgotten fields.
	t.Run("logout xai", func(t *testing.T) {
		home := withTempHome(t)
		creds := filepath.Join(home, ".roca", "credentials")
		if err := provider.SaveAPIKey(creds, provider.NameXAI, "sk-to-forget"); err != nil {
			t.Fatal(err)
		}
		out, err := runRootErr(t, Build{Version: "test"}, nil, "logout", "--json", "xai")
		if err != nil {
			t.Fatalf("logout --json: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("invalid JSON:\n%s", out)
		}
		if p, _ := result["provider"].(string); p != "xai" {
			t.Errorf("provider: %q, want xai", p)
		}
		if forgotten, _ := result["forgotten"].(bool); !forgotten {
			t.Error("forgotten field missing or not true")
		}
		if _, err := os.Stat(provider.APIKeyPath(creds, provider.NameXAI)); !os.IsNotExist(err) {
			t.Fatalf("the key is still on disk: %v", err)
		}
	})
}

// `roca logout xai` forgets the stored key. Forgetting what was already
// forgotten is not a failure.
func TestKeyLogoutRemovesTheCredential(t *testing.T) {
	home := isolatedLoginHome(t)
	creds := filepath.Join(home, ".roca", "credentials")
	if err := provider.SaveAPIKey(creds, provider.NameXAI, "sk-to-forget"); err != nil {
		t.Fatal(err)
	}

	out := runRoot(t, Build{Version: "test"}, "logout", "xai")
	if !strings.Contains(out, "forgotten") {
		t.Fatalf("no confirmation: %q", out)
	}
	if _, err := os.Stat(provider.APIKeyPath(creds, provider.NameXAI)); !os.IsNotExist(err) {
		t.Fatalf("the key is still on disk: %v", err)
	}

	if _, err := runRootErr(t, Build{Version: "test"}, nil, "logout", "xai"); err != nil {
		t.Fatalf("second logout: %v", err)
	}
}

func isolatedLoginHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{
		"ROCA_DB_PATH", "ROCA_CONFIG", "ROCA_MODELS_ORDER", "ROCA_CODEX_MODEL",
		"ROCA_OLLAMA_MODEL", "ROCA_MODEL", "DEEPSEEK_API_KEY", "ZAI_API_KEY", "XAI_API_KEY",
	} {
		t.Setenv(key, "")
	}
	return home
}

func bareLoginWithoutSession(t *testing.T, home string) string {
	t.Helper()
	store := provider.CodexStore(filepath.Join(home, ".roca", "credentials"))
	token, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	out := runRoot(t, Build{Version: "test"}, "login")
	if err := store.Save(token); err != nil {
		t.Fatal(err)
	}
	return out
}
