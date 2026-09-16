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

	want := "sudo chown operator '" + lock + "'"
	human := runRoot(t, contractBuild(), "doctor")
	for _, fragment := range []string{"state file owned by root: " + lock, want} {
		if !strings.Contains(human, fragment) {
			t.Fatalf("doctor narration missing %q:\n%s", fragment, human)
		}
	}

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
	reportJSON := mustJSON(t, runRoot(t, contractBuild(), "doctor", "--report", "--json"))
	if reportJSON["foreign_owned_count"] != float64(1) {
		t.Fatalf("support report foreign_owned_count = %#v, want 1", reportJSON["foreign_owned_count"])
	}
	if _, disclosed := reportJSON["foreign_owned"]; disclosed {
		t.Fatalf("support report disclosed foreign_owned: %#v", reportJSON["foreign_owned"])
	}
}

func TestDoctorReportsOwnershipWhenServiceCannotOpenState(t *testing.T) {
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
	want := "sudo chown operator '" + lock + "'"
	if !strings.Contains(out, want) {
		t.Fatalf("doctor error path missing %q:\n%s", want, out)
	}
}

func TestCLIRefusesRootOverUserOwnedState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root-over-user refusal is a unix check")
	}
	home := hermeticHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".roca"), 0o700); err != nil {
		t.Fatal(err)
	}
	restore := securefile.OverrideEffectiveUID(0)
	t.Cleanup(restore)

	_, err := runRootErr(t, contractBuild(), nil, "init")
	if err == nil {
		t.Fatal("root over user-owned state was accepted")
	}
	for _, want := range []string{"running as root", filepath.Join(home, ".roca")} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refuse error %q does not carry %q", err, want)
		}
	}

	version := runRoot(t, contractBuild(), "--version")
	if !strings.Contains(version, "roca") {
		t.Fatalf("version as root should still answer: %s", version)
	}
}
