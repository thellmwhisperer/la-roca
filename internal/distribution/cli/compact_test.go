package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompactRefusesEveryReadOnlyControlBeforeOpeningTheTarget(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		flags []string
		env   string
	}{
		{name: "flag", flags: []string{"--read-only"}},
		{name: "environment", env: "1"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("ROCA_READ_ONLY", testCase.env)
			target := filepath.Join(t.TempDir(), "untouched.db")
			args := append(testCase.flags, "compact", target)
			env := &cliEnv{build: Build{Version: "test"}, out: io.Discard, errOut: io.Discard}
			code, err := executeWithEnv(env, args, nil)
			if err == nil || code == ExitOK || !strings.Contains(err.Error(), "read-only") {
				t.Fatalf("compact read-only = code %d err %v", code, err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("read-only compact touched target: %v", err)
			}
		})
	}
}
