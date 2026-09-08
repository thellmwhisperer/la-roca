//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Core tests own only the executable protocol. Provider detection and human
// answering scenarios run in the plugin repository, through its real installer.
func installPlaygroundForAcceptance(home string) error {
	dir := filepath.Join(home, ".roca", "plugins", "roca-playground")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "roca-playground"), []byte(`#!/bin/sh
case "$1" in
 capabilities) printf '%s\n' '[{"ID":"retired-provider","RetiredProvider":"xai","Proposal":{"Alert":"synthetic retired xai provider"}}]' ;;
 probe) printf '%s\n' '{"warnings":["synthetic retired xai provider"]}' ;;
 *) printf 'unexpected playground fixture command: %s\n' "$1" >&2; exit 2 ;;
esac
`), 0700)
}

// This is intentionally outside ordinary core checks. It downloads a fixed
// release through the real installer, never a mutable source branch.
func TestPlaygroundPinnedReleaseIntegration(t *testing.T) {
	if os.Getenv("ROCA_PLAYGROUND_INTEGRATION") != "1" {
		t.Skip("opt-in pinned release integration: make playground-integration")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOARCH == "amd64" {
		platform = runtime.GOOS + "-x64"
	}
	source := "https://github.com/thellmwhisperer/roca-playground/releases/download/v0.1.1/roca-playground-v0.1.1-" + platform + ".tar.gz"
	m := aWorldIn(t, "playground-release")
	if err := m.runInit(); err != nil || m.last.code != 0 {
		t.Fatalf("init: %v %s", err, m.last.stderr)
	}
	out, code := m.runUnder(t, nil, "plugin", "install", source, "--yes", "--json")
	if code != 0 {
		t.Fatalf("pinned release installer: %d %s", code, out)
	}
	config := `[models]
order = ["fixture"]
[models.fixture]
command = ["/bin/sh", "-c", "printf 'SELECT 7 AS value\\n'"]
model = "fixture"
`
	if err := os.WriteFile(filepath.Join(m.home, ".roca", "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	out, code = m.runUnder(t, []string{"ROCA_MODELS_ORDER=fixture"}, "playground", "synthetic question", "--json")
	if code != 0 || !strings.Contains(out, `"value": 7`) {
		t.Fatalf("installed release result: %d %s", code, out)
	}
}
