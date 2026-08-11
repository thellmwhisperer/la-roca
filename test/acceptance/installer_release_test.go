//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
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
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, err := os.MkdirTemp("", "roca-pretty-release-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	// TMPDIR lives inside the HOME the way the suite's own scenarios set it up,
	// because the installer honours it when it builds its working directory.
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}

	m := &world{binary: binary, home: home}
	channel := m.theChannel()
	channel.prettyJSON = true

	if err := m.iRunTheInstaller(); err != nil {
		t.Fatalf("the installer could not be run: %v", err)
	}
	if m.last.code != 0 {
		t.Fatalf("the installer exited %d against a pretty-printed release:\n%s%s",
			m.last.code, m.last.stdout, m.last.stderr)
	}
	if err := m.exactlyOneExecutableRoca(); err != nil {
		t.Fatalf("the artefact did not land: %v", err)
	}
	if err := m.versionExitsWith(0); err != nil {
		t.Fatalf("the installed binary does not answer --version: %v", err)
	}
}
