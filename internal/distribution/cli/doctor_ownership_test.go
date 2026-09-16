package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestDoctorPrintsChownForForeignOwnedState(t *testing.T) {
	// CI cannot create a foreign-owned state tree without sudo, and the issue
	// contract forbids touching the operator's real ~/.roca. Exercise doctor on
	// a disposable installation while replacing only filesystem owner lookup.
	fixture := fixtureInstallation(t)
	lock := filepath.Join(fixture.home, ".roca", "plugins", "roca-vector", "state", "vector.db.index.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := securefile.OverrideIdentityLookups(
		securefile.Identity{UID: 501, Name: "operator"},
		map[string]securefile.Identity{lock: {UID: 0, Name: "root"}},
	)
	t.Cleanup(restore)

	want := "sudo chown -h operator '" + lock + "'"
	human := runRoot(t, contractBuild(), "doctor")
	for _, fragment := range []string{"state file owned by root: " + lock, want} {
		if !strings.Contains(human, fragment) {
			t.Fatalf("doctor narration missing %q:\n%s", fragment, human)
		}
	}
	redactedHuman := strings.ReplaceAll(human, fixture.home, "$HOME")
	redactedHuman = strings.ReplaceAll(redactedHuman, os.TempDir(), "$TMPDIR")
	t.Logf("local doctor ownership diagnosis:\n%s", redactedHuman)

	doc := mustJSON(t, runRoot(t, contractBuild(), "doctor", "--json"))
	owned, _ := doc["foreign_owned"].([]any)
	if len(owned) != 1 {
		t.Fatalf("foreign_owned = %#v, want the lock", doc["foreign_owned"])
	}
	row, _ := owned[0].(map[string]any)
	if row["chown"] != want || row["owner"] != "root" || row["path"] != lock {
		t.Fatalf("foreign_owned row = %#v, want chown %q", row, want)
	}

	report := runRoot(t, contractBuild(), "doctor", "--report")
	for _, forbidden := range []string{want, lock, "root", "operator"} {
		if strings.Contains(report, forbidden) {
			t.Fatalf("support report disclosed %q:\n%s", forbidden, report)
		}
	}
	if !strings.Contains(report, "state ownership findings: 1") {
		t.Fatalf("support report omitted ownership count:\n%s", report)
	}
	t.Logf("privacy-safe doctor support report:\n%s", report)
	reportJSON := mustJSON(t, runRoot(t, contractBuild(), "doctor", "--report", "--json"))
	if reportJSON["foreign_owned_count"] != float64(1) {
		t.Fatalf("support report foreign_owned_count = %#v, want 1", reportJSON["foreign_owned_count"])
	}
	if _, disclosed := reportJSON["foreign_owned"]; disclosed {
		t.Fatalf("support report disclosed foreign_owned: %#v", reportJSON["foreign_owned"])
	}
}

func TestDoctorReportsOwnershipWhenServiceCannotOpenState(t *testing.T) {
	// Use the same ownership seam: a real root-owned fixture would require the
	// forbidden sudo setup, while the CLI and its pre-open ordering remain real.
	home := hermeticHome(t)
	lock := filepath.Join(home, ".roca", "vector.db.index.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := securefile.OverrideIdentityLookups(
		securefile.Identity{UID: 501, Name: "operator"},
		map[string]securefile.Identity{lock: {UID: 0, Name: "root"}},
	)
	t.Cleanup(restore)

	out, err := runRootErr(t, contractBuild(), nil, "doctor")
	if err == nil {
		t.Fatal("doctor unexpectedly opened an uninitialized state")
	}
	want := "sudo chown -h operator '" + lock + "'"
	if !strings.Contains(out, want) {
		t.Fatalf("doctor error path missing %q:\n%s", want, out)
	}

	report := runRoot(t, contractBuild(), "doctor", "--report")
	if !strings.Contains(report, "state ownership findings: 1") {
		t.Fatalf("doctor --report on uninitialized state omitted the privacy-safe ownership count:\n%s", report)
	}
}

func TestCLIRefusesRootOverUserOwnedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root-over-user refusal is a unix check")
	}
	// A real foreign owner would require sudo and violate the issue contract.
	// Keep HOME disposable and replace only identity lookup while exercising the
	// shared CLI boundary for both a directory and a dereferenced state symlink.
	for _, testCase := range []struct {
		name    string
		symlink bool
	}{
		{name: "directory"},
		{name: "symlink", symlink: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := hermeticHome(t)
			state := filepath.Join(home, ".roca")
			ownedPath := state
			if testCase.symlink {
				ownedPath = t.TempDir()
				if err := os.Symlink(ownedPath, state); err != nil {
					t.Fatal(err)
				}
				resolved, err := filepath.EvalSymlinks(ownedPath)
				if err != nil {
					t.Fatal(err)
				}
				ownedPath = resolved
			} else if err := os.MkdirAll(state, 0o700); err != nil {
				t.Fatal(err)
			}
			restore := securefile.OverrideIdentityLookups(
				securefile.Identity{UID: 0, Name: "root"},
				map[string]securefile.Identity{ownedPath: {UID: 501, Name: "operator"}},
			)
			defer restore()

			_, err := runRootErr(t, contractBuild(), nil, "init")
			if err == nil {
				t.Fatal("root over user-owned state was accepted")
			}
			for _, want := range []string{"running as root", state} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refuse error %q does not carry %q", err, want)
				}
			}
			t.Logf("CLI root-over-user refusal: %s", strings.ReplaceAll(err.Error(), home, "$HOME"))

			version := runRoot(t, contractBuild(), "--version")
			if !strings.Contains(version, "roca") {
				t.Fatalf("version as root should still answer: %s", version)
			}
		})
	}
}

func TestCLISharedBoundaryGuardsParsingAndReadOnlyInvocations(t *testing.T) {
	home := hermeticHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".roca"), 0o700); err != nil {
		t.Fatal(err)
	}
	restore := securefile.OverrideEffectiveUID(0)
	t.Cleanup(restore)

	for _, testCase := range []struct {
		name      string
		args      []string
		expectErr bool
	}{
		{name: "parsing", args: []string{"init", "--unknown"}, expectErr: true},
		{name: "command help", args: []string{"query", "--help"}},
		{name: "short help", args: []string{"-h"}},
		{name: "root help"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out, errOut strings.Builder
			env := &cliEnv{build: contractBuild(), out: &out, errOut: &errOut}
			_, err := executeWithOptions(env, testCase.args, nil, false)
			if (err != nil) != testCase.expectErr {
				t.Fatalf("invocation %v error = %v, want error %v", testCase.args, err, testCase.expectErr)
			}
			if _, statErr := os.Stat(filepath.Join(home, ".roca", "logs")); !os.IsNotExist(statErr) {
				t.Fatalf("invocation %v created execution logs: %v", testCase.args, statErr)
			}
		})
	}
}
