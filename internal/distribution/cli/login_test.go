package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider"
)

func TestBareLoginListsOnlyLocalCLIsAndNoCredentialState(t *testing.T) {
	home := isolatedLoginHome(t)
	configPath := filepath.Join(home, ".roca", "config.toml")
	out := runRoot(t, Build{Version: "test"}, "login")
	want := "Supported providers:\n" +
		"  codex       local CLI     ·  roca login codex\n" +
		"  claude      local CLI     ·  roca login claude\n" +
		"Model configuration:\n" +
		"  order: codex, ollama (built-in default · change with: models.order in " + configPath + ")\n" +
		"  codex: model " + provider.DefaultCodexModel + " (built-in default · change with: roca model set <id> or models.codex.model in " + configPath + ")\n" +
		"  ollama: model qwen3.5:4b (built-in default · change with: models.ollama.model in " + configPath + ")\n" +
		"Authentication: models authenticate through their own CLIs; La Roca stores no secrets and no roca login is required."
	if out != want {
		t.Fatalf("bare login output changed:\n--- want ---\n%s\n--- got ---\n%s", want, out)
	}
}

func TestLoginHelpExplainsThatOnlyLocalCLIsAuthenticate(t *testing.T) {
	_ = isolatedLoginHome(t)
	out := runRoot(t, Build{Version: "test"}, "login", "--help")
	lower := strings.ToLower(out)
	for _, retired := range []string{"api key", "oauth", "credentials directory", "credential and session state"} {
		if strings.Contains(lower, retired) {
			t.Errorf("login help still mentions %q:\n%s", retired, out)
		}
	}
	for _, want := range []string{"authenticate through their own cli", "no roca login is required", "roca login codex", "roca login claude"} {
		if !strings.Contains(lower, want) {
			t.Errorf("login help omitted %q:\n%s", want, out)
		}
	}

	_, err := runRootErr(t, Build{Version: "test"}, nil, "login", "xai")
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "authenticate through their own cli") {
		t.Fatalf("retired provider error = %v", err)
	}
}

func TestLocalCLILoginVerifiesWithoutStoringASecret(t *testing.T) {
	home := isolatedLoginHome(t)
	bin := os.Getenv("PATH")
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nprintf 'SELECT 1'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runRootErr(t, Build{Version: "test"}, nil,
		"login", "--json", "--model", provider.DefaultCodexModel, "codex")
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if result["provider"] != provider.NameCodex || result["secrets_stored_by_roca"] != false ||
		result["authentication_managed_by"] != "local CLI" {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", "credentials")); !os.IsNotExist(err) {
		t.Fatalf("credential directory exists: %v", err)
	}
}

func TestBareLoginJSONContainsNoCredentialState(t *testing.T) {
	_ = isolatedLoginHome(t)
	out, err := runRootErr(t, Build{Version: "test"}, nil, "login", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if _, exists := result["credentials"]; exists {
		t.Fatalf("credential state survived: %+v", result)
	}
	providers, _ := result["providers"].([]any)
	if len(providers) != 2 {
		t.Fatalf("providers = %+v", providers)
	}
}

func isolatedLoginHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, key := range []string{
		"ROCA_DB_PATH", "ROCA_CONFIG", "ROCA_MODELS_ORDER", "ROCA_CODEX_MODEL",
		"ROCA_OLLAMA_MODEL", "ROCA_MODEL",
	} {
		t.Setenv(key, "")
	}
	return home
}
