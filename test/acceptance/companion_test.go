//go:build acceptance

package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestServeRaisesAndReapsPluginCompanions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("session companions are proven on unix stdio")
	}

	t.Run("a write lands then both sessions stop", func(t *testing.T) {
		m := companionLab(t)
		watch := filepath.Join(m.home, "inbox")
		out := filepath.Join(m.home, "mirror.txt")
		if err := os.Mkdir(watch, 0o700); err != nil {
			t.Fatal(err)
		}
		installLabCompanion(t, m, writeCompanionPackage(t, m.home, watch, out, filepath.Join(m.home, "lock")))
		serve, held := startServe(t, m)
		if err := os.WriteFile(filepath.Join(watch, "note.md"), []byte("harbor lantern\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, func() bool {
			raw, err := os.ReadFile(out)
			return err == nil && strings.Contains(string(raw), "harbor lantern")
		})
		if err := m.mustRun("roca health"); err != nil {
			t.Fatalf("health during companion: %v\n%s", err, m.last.stderr)
		}
		second, secondHeld := startServe(t, m)
		waitUntil(t, func() bool {
			_, err := os.Stat(filepath.Join(m.home, "standby"))
			return err == nil
		})
		if countDistinctFiles(t, filepath.Join(m.home, "holder"), filepath.Join(m.home, "standby")) != 2 {
			t.Fatal("plugin single-flight did not leave one holder and one standby")
		}
		_ = held.Close()
		_ = serve.Wait()
		_ = secondHeld.Close()
		_ = second.Wait()
		waitUntil(t, func() bool {
			_, err := os.Stat(filepath.Join(m.home, "dead"))
			return err == nil
		})
	})

	t.Run("a missing executable leaves health working", func(t *testing.T) {
		m := companionLab(t)
		installLabCompanion(t, m, writeCompanionPackageMissingBinary(t, m.home))
		if err := os.Remove(filepath.Join(m.home, ".roca", "plugins", "mirror", "roca-mirror")); err != nil {
			t.Fatal(err)
		}
		serve, held := startServe(t, m)
		if err := m.mustRun("roca health"); err != nil {
			t.Fatalf("health with missing companion: %v\n%s", err, m.last.stderr)
		}
		_ = held.Close()
		if err := serve.Wait(); err != nil {
			t.Fatalf("serve with missing companion: %v", err)
		}
	})
}

func companionLab(t *testing.T) *world {
	t.Helper()
	m := aWorldIn(t, strings.ReplaceAll(t.Name(), "/", "-"))
	if err := m.runInit(); err != nil {
		t.Fatalf("init: %v", err)
	}
	if m.last.code != 0 {
		t.Fatalf("init: code %d\n%s", m.last.code, m.last.stderr)
	}
	return m
}

func installLabCompanion(t *testing.T, m *world, source string) {
	t.Helper()
	if err := m.mustRun("roca plugin install --yes " + source); err != nil {
		t.Fatalf("plugin install: %v\n%s\n%s", err, m.last.stdout, m.last.stderr)
	}
}

func countDistinctFiles(t *testing.T, paths ...string) int {
	t.Helper()
	seen := map[string]bool{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		seen[strings.TrimSpace(string(raw))] = true
	}
	return len(seen)
}

func startServe(t *testing.T, m *world) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	serve := exec.Command(m.binaryPath(), "mcp", "serve")
	serve.Env = m.environment()
	held, err := serve.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	serve.Stdout = os.Stdout
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("roca mcp serve: %v", err)
	}
	t.Cleanup(func() { held.Close(); serve.Process.Kill(); serve.Wait() })
	return serve, held
}

func writeCompanionPackage(t *testing.T, home, watch, out, lock string) string {
	t.Helper()
	source := filepath.Join(home, "src", "mirror")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if ! mkdir " + lock + " 2>/dev/null; then echo $$ > " + filepath.Join(home, "standby") +
		"; trap 'echo gone >> " + filepath.Join(home, "dead") + "' EXIT; while IFS= read -r line; do :; done; exit 0; fi\n" +
		"echo $$ > " + filepath.Join(home, "holder") + "\n" +
		"( while :; do if [ -f \"$1/note.md\" ]; then cat \"$1/note.md\" > \"$2\"; fi; sleep 0.05; done ) &\n" +
		"kid=$!\n" +
		"trap 'kill $kid; rmdir " + lock + "; echo gone >> " + filepath.Join(home, "dead") + "' EXIT\n" +
		"while IFS= read -r line; do :; done\n"
	writePackageFiles(t, source, script, `["`+watch+`", "`+out+`"]`)
	return source
}

func writeCompanionPackageMissingBinary(t *testing.T, home string) string {
	t.Helper()
	source := filepath.Join(home, "src", "mirror")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writePackageFiles(t, source, "#!/bin/sh\nexit 0\n", `[]`)
	return source
}

func writePackageFiles(t *testing.T, source, script, args string) {
	t.Helper()
	manifest := fmt.Sprintf(`{
  "schema": 1,
  "name": "mirror",
  "version": "1.0.0",
  "kind": "executable",
  "companion": {"executable": "roca-mirror", "args": %s}
}
`, args)
	if err := os.WriteFile(filepath.Join(source, "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "roca-mirror"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	var checksums strings.Builder
	for _, name := range []string{"plugin.json", "roca-mirror"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	if err := os.WriteFile(filepath.Join(source, "checksums.txt"), []byte(checksums.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}
