package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestFirstTTYCommandOffersOpenCapabilitiesOnceForTheBuild(t *testing.T) {
	cases := []struct {
		name, input, wantErr string
		interactive          bool
		args                 []string
	}{
		{name: "successful TTY command", input: "n\n", interactive: true, args: []string{"version"}},
		{name: "failed non-TTY command", wantErr: "no Roca database", args: []string{"query", "what changed?"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, bin := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", bin)
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("fixture"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, ".roca", "config.toml")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("[models]\norder = [\"ollama\"]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			previous := terminalInput
			terminalInput = func(any) bool { return tc.interactive }
			t.Cleanup(func() { terminalInput = previous })

			first, err := runCapabilityRoot(t, Build{Version: "v2", Commit: "sha"},
				strings.NewReader(tc.input), tc.args...)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) ||
				!strings.Contains(first, "Claude Code is on PATH") {
				t.Fatalf("first command: %v\n%s", err, first)
			}
			second, err := runCapabilityRoot(t, Build{Version: "v2", Commit: "sha"},
				strings.NewReader(tc.input), tc.args...)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) ||
				strings.Contains(second, "Claude Code is on PATH") {
				t.Fatalf("same-version command repeated proposal: %v\n%s", err, second)
			}
		})
	}
}

func runCapabilityRoot(t *testing.T, build Build, in *strings.Reader, args ...string) (string, error) {
	t.Helper()
	var output strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: build, out: &output, errOut: &output})
	env.skipReconciliation = false
	_, err := executeWithEnv(env, args, in)
	return output.String(), err
}

func TestDoctorListsAnOpenProposalEvenAfterItsVersionStamp(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	isolateRuntimeDirs(t, home)
	t.Setenv("PATH", bin)
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, ".roca", "roca.db")
	runRoot(t, contractBuild(), "init", "--db-path", dbPath)
	path := filepath.Join(home, ".roca", "config.toml")
	text := "[models]\norder = [\"ollama\"]\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Reconciliation,
		[]byte(`{"claude-cli-provider":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runRoot(t, contractBuild(), "doctor")
	if !strings.Contains(out, "open capability proposals") ||
		!strings.Contains(out, "Claude Code is on PATH") {
		t.Fatalf("doctor hid stamped open proposal:\n%s", out)
	}
}

func TestUpdateResultNamesPendingCapabilityCount(t *testing.T) {
	var out strings.Builder
	env := &cliEnv{out: &out, errOut: &out}
	if err := env.reportUpdate(map[string]any{"updated": true}, 2, "updated"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2 new capabilities need a look: run `roca doctor`") {
		t.Fatalf("update result = %q", out.String())
	}
}
