package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeAcceptanceAndE2ESmokePinTheBuiltBinaryOverInheritedROCABin(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "roca-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho STUB\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		target string
		bin    string
	}{
		{target: "accept", bin: "bin/roca"},
		{target: "e2e-smoke", bin: "bin/roca"},
		{target: "e2e-smoke", bin: ".tmp/roca"},
	} {
		t.Run(test.target+"/"+test.bin, func(t *testing.T) {
			cmd := exec.Command("make", "-n", "--no-print-directory", test.target, "BIN="+test.bin)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "ROCA_BIN="+stub)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n %s: %v\n%s", test.target, err, out)
			}
			recipe := pinnedTestRecipe(t, string(out))
			if !strings.Contains(recipe, "ROCA_BIN="+test.bin) {
				t.Fatalf("make -n %s BIN=%s did not pin ROCA_BIN=%s\n%s", test.target, test.bin, test.bin, recipe)
			}
			if strings.Contains(recipe, stub) {
				t.Fatalf("inherited stub selected a different binary\n%s", recipe)
			}
		})
	}
}

func pinnedTestRecipe(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "go test -tags=acceptance") && strings.Contains(line, "ROCA_BIN=") {
			return line
		}
	}
	t.Fatalf("make -n printed no pinned acceptance test recipe\n%s", output)
	return ""
}
