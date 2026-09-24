//go:build acceptance

package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeTargetsSelectTheirRequiredBinary(t *testing.T) {
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

	for _, target := range []string{"accept", "e2e-smoke", "e2e-federation"} {
		t.Run(target, func(t *testing.T) {
			expected, build := "BUILT", ":"
			if target == "e2e-federation" {
				expected, build = "STUB", "false"
			}
			cmd := exec.Command("make", "--no-print-directory", target, "BIN="+relativeBuilt,
				"VECTOR_BUILD="+build, "GO_BUILD="+build, "VECTOR_BUNDLE="+build)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+fakeTools+string(os.PathListSeparator)+os.Getenv("PATH"),
				"REAL_GO="+goTool,
				"ROCA_BIN="+stub,
				"ROCA_PUBLISHED_BIN="+built,
				"ROCA_E2E_VECTOR_MODEL="+built,
				"ROCA_MAKE_BIN_PIN_PROBE=1",
				"ROCA_MAKE_BIN_PIN_EXPECTED="+expected,
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
	if got, want := strings.TrimSpace(string(out)), os.Getenv("ROCA_MAKE_BIN_PIN_EXPECTED"); got != want {
		t.Fatalf("selected binary output %q, want %q", got, want)
	}
}

func TestMakeE2ERequiresExplicitPublishedBinary(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	temp, err := acceptanceTempDir("make-published-pin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(temp) })
	for _, name := range []string{"roca", "go"} {
		if err := os.WriteFile(filepath.Join(temp, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "ROCA_PUBLISHED_BIN=") && !strings.HasPrefix(entry, "PATH=") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "PATH="+temp+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, target := range []string{"e2e-smoke", "e2e-federation"} {
		t.Run(target, func(t *testing.T) {
			cmd := exec.Command("make", "--no-print-directory", "-o", "build", target,
				"ROCA_E2E_VECTOR_MODEL="+filepath.Join(temp, "roca"))
			cmd.Dir, cmd.Env = root, environment
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), "set ROCA_PUBLISHED_BIN") {
				t.Fatalf("make %s must require an explicit published binary: %v\n%s", target, err, out)
			}
		})
	}
}

func TestMakeFederationRequiresInstalledCandidate(t *testing.T) {
	root, err := acceptanceRoot()
	if err != nil {
		t.Fatal(err)
	}
	temp, err := acceptanceTempDir("make-candidate-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(temp) })
	published := filepath.Join(temp, "published")
	if err := os.WriteFile(published, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", filepath.Join(temp, "missing")} {
		cmd := exec.Command("make", "--no-print-directory", "e2e-federation",
			"ROCA_BIN="+candidate, "ROCA_PUBLISHED_BIN="+published,
			"ROCA_E2E_VECTOR_MODEL="+published, "VECTOR_BUILD=false", "GO_BUILD=false", "VECTOR_BUNDLE=false")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "set ROCA_BIN to an installed candidate") {
			t.Fatalf("make must require an installed candidate: %v\n%s", err, out)
		}
	}
}
