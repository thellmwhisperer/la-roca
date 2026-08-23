package cli

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestInitNarratesItsPhasesAndPointsToThePromptLast(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "none")

	out := runRoot(t, Build{Version: "test", Commit: "test-sha"},
		"init", "--db-path", filepath.Join(home, ".roca", "roca.db"))
	for _, want := range []string{
		"setup:",
		"agents: checking known sources",
		"agents detected:",
		"agents not found:",
		"database: inspecting",
		"database outcome: created",
		"rows: memories=",
		"ingest:",
		"delta:",
		"index: full-text index ready",
		"model:",
		"total:",
		"next steps:",
		"data directory:",
		"configuration:",
		"agent prompt:",
		"Paste its contents into the agent instructions you choose.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init narration does not carry %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "## La Roca — local semantic memory") ||
		strings.Contains(out, "La Roca never edits agent instruction files") {
		t.Errorf("init dumped prompt.md instead of pointing to it:\n%s", out)
	}
}

func TestSemanticConsentConsumesStructuredWorkerResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	companion := filepath.Join(bin, "roca-vector")
	fixture := "#!/bin/sh\nprintf '%s\\n' '{\"background\":true,\"pid\":4242,\"log_path\":\"/private/operator/path\"}'\nprintf '%s\\n' 'raw worker detail' >&2\n"
	if err := os.WriteFile(companion, []byte(fixture), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	var out, errOut bytes.Buffer
	env := &cliEnv{out: &out, errOut: &errOut, dbPath: filepath.Join(root, "roca.db")}
	paths := config.Paths{Config: filepath.Join(root, "config.toml")}
	input := bufio.NewReader(strings.NewReader("yes\n"))
	if err := env.offerSemanticSearch(context.Background(), input, true, paths); err != nil {
		t.Fatal(err)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "setup continues in the background") {
		t.Fatalf("semantic setup output = %q", combined)
	}
	for _, detail := range []string{"4242", "/private/operator/path", "raw worker detail"} {
		if strings.Contains(combined, detail) {
			t.Fatalf("semantic setup output leaked %q: %q", detail, combined)
		}
	}
}
