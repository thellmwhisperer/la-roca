package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func hermeticCLIEnv(env *cliEnv) *cliEnv {
	env.skipReconciliation = true
	env.skipInitChooser = true
	return env
}

func writeConfig(t *testing.T, home, body string) string {
	t.Helper()
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
