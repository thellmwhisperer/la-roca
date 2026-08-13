package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginHelpDeclaresTheReadOnlyCompatibilityAlias(t *testing.T) {
	_ = isolatedLoginHome(t)
	out := runRoot(t, Build{Version: "test"}, "login", "--help")
	lower := strings.ToLower(out)
	for _, retired := range []string{"api key", "oauth", "credentials directory", "model set", "--model"} {
		if strings.Contains(lower, retired) {
			t.Errorf("login help still mentions %q:\n%s", retired, out)
		}
	}
	for _, want := range []string{"compatibility alias", "roca model check", "never writes configuration", "authenticate through their own cli"} {
		if !strings.Contains(lower, want) {
			t.Errorf("login help omitted %q:\n%s", want, out)
		}
	}
}

func TestLoginModelFlagIsRetiredWithoutWriting(t *testing.T) {
	home := isolatedLoginHome(t)
	path := modelConfigPath(home)
	before := "[models]\norder = [\"ollama\", \"codex\"]\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runRootErr(t, Build{Version: "test"}, nil, "login", "codex", "--model", "invented")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --model") {
		t.Fatalf("error = %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != before {
		t.Fatalf("retired flag changed config: raw=%q err=%v", raw, readErr)
	}
}

func TestBareLoginJSONIsAReadOnlyModelCheck(t *testing.T) {
	home := isolatedLoginHome(t)
	out, err := runRootErr(t, Build{Version: "test"}, nil, "login", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["provider"] != "codex" || result["ready"] != true || result["configuration_changed"] != false {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("model check wrote config: %v", err)
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
