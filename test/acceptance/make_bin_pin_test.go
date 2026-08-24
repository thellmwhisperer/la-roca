//go:build acceptance

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeAcceptanceAndE2ESmokePinTheBuiltBinaryOverInheritedROCABin(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	temp, err := acceptanceTempDir("make-bin-pin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(temp) })

	built := filepath.Join(temp, "roca-built")
	stub := filepath.Join(temp, "roca-stub")
	fakeTools := filepath.Join(temp, "tools")
	if err := os.Mkdir(fakeTools, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(built, []byte("#!/bin/sh\nprintf 'BUILT\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'STUB\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	goWrapper := filepath.Join(fakeTools, "go")
	if err := os.WriteFile(goWrapper, []byte("#!/bin/sh\nexec \"$REAL_GO\" test -tags=acceptance ./test/acceptance -run '^TestMakeBinPinProbe$' -count=1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	relativeBuilt, err := filepath.Rel(root, built)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"accept", "e2e-smoke"} {
		t.Run(target, func(t *testing.T) {
			cmd := exec.Command("make", "--no-print-directory", target, "BIN="+relativeBuilt, "VECTOR_BUILD=:", "GO_BUILD=:", "VECTOR_BUNDLE=:")
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+fakeTools+string(os.PathListSeparator)+os.Getenv("PATH"),
				"REAL_GO="+goTool,
				"ROCA_BIN="+stub,
				"ROCA_MAKE_BIN_PIN_PROBE=1",
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make %s: %v\n%s", target, err, out)
			}
		})
	}
}

func TestMakeBinPinProbe(t *testing.T) {
	if os.Getenv("ROCA_MAKE_BIN_PIN_PROBE") != "1" {
		t.Skip("make binary pin probe")
	}
	binary, err := rocaBinary()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("execute selected binary %s: %v\n%s", binary, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "BUILT" {
		t.Fatalf("selected binary output %q, want BUILT", got)
	}
}
