//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The channel serves the release document the way GitHub actually serves it:
// pretty-printed, with every field on its own line. The installer's asset
// parser exists to survive that shape, and this test is the in-repo guard that
// it does. Without it the only evidence was the `/tmp` transcripts in the PR
// body, which are not a regression guard: if the parser were ever "simplified"
// back to a splitter that only copes with compact JSON, nothing here would go
// red.
//
// The harness knob (`installWorld.prettyJSON`) makes `writeRelease` emit
// MarshalIndent, and this test is what exercises it.
func TestTheInstallerResolvesAPrettyPrintedReleaseDocument(t *testing.T) {
	m := releaseInstallerWorld(t)
	channel := m.theChannel()
	channel.prettyJSON = true

	if err := m.iRunTheInstaller(); err != nil {
		t.Fatalf("the installer could not be run: %v", err)
	}
	if m.last.code != 0 {
		t.Fatalf("the installer exited %d against a pretty-printed release:\n%s%s",
			m.last.code, m.last.stdout, m.last.stderr)
	}
	if err := m.onlyRocaExecutables(); err != nil {
		t.Fatalf("the artefact did not land: %v", err)
	}
	if err := m.versionExitsWith(0); err != nil {
		t.Fatalf("the installed binary does not answer --version: %v", err)
	}
}

func TestTheInstallerRestoresThePreviousBinaryWhenBundledPlacementFails(t *testing.T) {
	m := releaseInstallerWorld(t)
	if err := m.iRunTheInstaller(); err != nil || m.last.code != 0 {
		t.Fatalf("initial install: %v, code %d:\n%s%s", err, m.last.code, m.last.stdout, m.last.stderr)
	}
	manifest := filepath.Join(m.home, ".roca", "plugins", "vector", ".roca-plugin.json")
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(body), `"source": "bundled:roca"`, `"source": "fixture:collision"`, 1)
	if changed == string(body) {
		t.Fatal("the installed manifest did not declare the bundled source")
	}
	if err := os.WriteFile(manifest, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.iRunTheInstallerOfTheNewVersion(); err != nil {
		t.Fatal(err)
	}
	failure := m.last.stdout + m.last.stderr
	if m.last.code == 0 || !strings.Contains(failure, "bundled plugins could not be placed") {
		t.Fatalf("bundled placement failure was not reported: code %d\n%s", m.last.code, failure)
	}
	if err := m.theVersionIsStillTheBuiltOne(); err != nil {
		t.Fatalf("the prior binary was not restored: %v", err)
	}
	if !strings.Contains(failure, "previous binary is back") {
		t.Fatalf("the rollback was not reported:\n%s", failure)
	}
}

func releaseInstallerWorld(t *testing.T) *world {
	t.Helper()
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &world{binary: binary, home: home}
	t.Cleanup(m.closeTheChannel)
	return m
}
