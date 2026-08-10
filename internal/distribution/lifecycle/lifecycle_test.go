package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The narration prints deleted children before their parent directory, so an
// operator sees what was inside before they are told the container is gone.
func TestAnOwnedDirectoryGoesWithWhatIsInsideIt(t *testing.T) {
	data := filepath.Join(t.TempDir(), ".roca")
	backups := makeDir(t, data, "backups")
	child := touch(t, backups, "roca-2026-08-05.db")

	report := Plan{Owned: []string{backups, child}, DataDir: data}.Apply()

	if _, err := os.Stat(backups); !os.IsNotExist(err) {
		t.Fatal("an owned directory with content survived")
	}
	if !report.Purged {
		t.Fatalf("%+v", report)
	}
	childIdx := slices.Index(report.Deleted, child)
	parentIdx := slices.Index(report.Deleted, backups)
	if childIdx < 0 || parentIdx < 0 {
		t.Fatalf("missing expected paths in %v", report.Deleted)
	}
	if childIdx > parentIdx {
		t.Errorf("order is %v: child must come before its parent directory", report.Deleted)
	}
}

// The purge deletes what it declares and says which paths those were. An
// operator who cannot read the list has no way to check the machine is clean.
func TestThePurgeDeletesWhatItDeclaresAndNamesEveryPath(t *testing.T) {
	home, data, database := anInstallation(t)
	settings := touch(t, data, "config.toml")
	backups := makeDir(t, data, "backups")
	binary := touch(t, filepath.Join(home, "bin"), "roca")

	report := Plan{
		Owned:   []string{database, settings, backups},
		DataDir: data,
		Binary:  binary,
	}.Apply()

	if !report.Purged {
		t.Fatalf("purged = false: %+v", report)
	}
	for _, path := range []string{database, settings, backups, binary, data} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still there", path)
		}
		if !slices.Contains(report.Deleted, path) {
			t.Errorf("%s was deleted and is not in the report", path)
		}
	}
}

// The second purge finds nothing and still succeeds because the job is already
// complete.
func TestThePurgeRunTwiceEndsOkBothTimes(t *testing.T) {
	_, data, database := anInstallation(t)
	plan := Plan{Owned: []string{database}, DataDir: data}

	if first := plan.Apply(); !first.Purged || len(first.Errors) > 0 {
		t.Fatalf("the first purge: %+v", first)
	}
	second := plan.Apply()
	if !second.Purged {
		t.Fatalf("the second purge reports purged = false: %+v", second)
	}
	if len(second.Errors) > 0 {
		t.Fatalf("the second purge failed over what was already gone: %v", second.Errors)
	}
	if len(second.Deleted) > 0 {
		t.Errorf("the second purge claims to have deleted %v", second.Deleted)
	}
}

// The inventory is a DECLARATION, not a snapshot taken before the command
// creates its own artefacts.
//
// The race is what was removed. A path Roca owns is deleted whenever it exists,
// no matter when it appeared.
func TestAnArtefactThatAppearsAfterThePlanIsStillPurged(t *testing.T) {
	_, data, database := anInstallation(t)
	cache := filepath.Join(data, "cache")

	plan := Plan{Owned: []string{database, cache}, DataDir: data}
	// Between the plan and its application, this installation creates its own
	// working directory. That is exactly the shape of the race.
	makeDir(t, data, "cache")

	report := plan.Apply()
	if !report.Purged {
		t.Fatalf("purged = false over its own artefact: %+v", report)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Error("the purge left its own working directory behind")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Error("the data directory survived because of its own artefact")
	}
}

// The protection that is NOT relaxed: a file Roca did not create is left where
// it is and reported by name, and the directory holding it survives with it.
func TestAFileRocaDidNotCreateIsKeptAndNamed(t *testing.T) {
	_, data, database := anInstallation(t)
	foreign := touch(t, data, "notes-of-mine.md")

	report := Plan{Owned: []string{database}, DataDir: data}.Apply()

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("a file Roca did not create was deleted: %v", err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatalf("the directory holding it was deleted: %v", err)
	}
	if !report.Purged {
		t.Errorf("purged = false: refusing to delete somebody else's file is not a failure")
	}
	var named bool
	for _, kept := range report.Kept {
		if strings.Contains(kept.Path, "notes-of-mine.md") || kept.Path == data {
			named = true
			if kept.Reason == "" {
				t.Errorf("%s was kept with no reason", kept.Path)
			}
		}
	}
	if !named {
		t.Errorf("nothing was reported as kept: %+v", report)
	}
}

// The binary is deleted because it is Roca's, and a file with another name
// where the binary was expected is not: the operator asked to uninstall Roca,
// not to delete whatever is at that path.
func TestABinaryThatIsNotRocaIsNotDeleted(t *testing.T) {
	dir := t.TempDir()
	stranger := touch(t, dir, "kubectl")

	report := Plan{Binary: stranger}.Apply()

	if _, err := os.Stat(stranger); err != nil {
		t.Fatalf("a binary that is not Roca's was deleted: %v", err)
	}
	if len(report.Kept) == 0 {
		t.Fatal("it was kept and not reported")
	}
	if !strings.Contains(report.Kept[0].Reason, "roca") {
		t.Errorf("the reason does not say what it expected: %q", report.Kept[0].Reason)
	}
}

// Keeping the data is the same command with nothing declared: the binary goes,
// the database stays, and nothing has to know about a second code path.
func TestKeepingTheDataLeavesTheDatabaseWhereItIs(t *testing.T) {
	home, _, database := anInstallation(t)
	binary := touch(t, filepath.Join(home, "bin"), "roca")

	report := Plan{Binary: binary}.Apply()

	if !report.Purged {
		t.Fatalf("%+v", report)
	}
	if _, err := os.Stat(database); err != nil {
		t.Fatalf("the database was deleted with --keep-data: %v", err)
	}
	if _, err := os.Stat(binary); !os.IsNotExist(err) {
		t.Error("the binary is still linked")
	}
}

// The bounded survivor list must not misclassify what it stops naming one by
// one. D-7's second half promises an owned survivor is reported as one the purge
// failed to remove, so the operator re-runs the uninstall instead of going and
// deleting product files by hand. The overflow line called every remaining file
// foreign, which is the exact misclassification that contract exists to prevent.
func TestTheSurvivorOverflowDoesNotCallOwnedFilesForeign(t *testing.T) {
	_, data, database := anInstallation(t)
	owned := []string{database}
	for i := range 8 {
		owned = append(owned, touch(t, data, fmt.Sprintf("owned-%d.db-wal", i)))
	}

	// The directory stops being writable, so every owned path survives the sweep:
	// these are the survivors the contract calls Roca's own.
	if err := os.Chmod(data, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(data, 0o700) })

	report := Plan{Owned: owned, DataDir: data}.Apply()

	overflow := ""
	for _, kept := range report.Kept {
		if kept.Path == data {
			overflow = kept.Reason
		}
	}
	if overflow == "" {
		t.Fatalf("no overflow line over %d survivors: %+v", len(owned), report.Kept)
	}
	if strings.Contains(overflow, "did not create") {
		t.Errorf("the overflow calls owned survivors foreign: %q", overflow)
	}
}

// Counted prose is counted prose everywhere: one leftover file is "1 more
// file", never "1 more files".
func TestTheSurvivorOverflowCountsInSingularWhenOnlyOneIsLeft(t *testing.T) {
	_, data, _ := anInstallation(t)
	for i := range 5 {
		touch(t, data, fmt.Sprintf("mine-%d.md", i))
	}

	report := Plan{DataDir: data}.Apply()

	for _, kept := range report.Kept {
		if kept.Path == data && !strings.Contains(kept.Reason, "1 more file") {
			t.Errorf("the overflow over six survivors reads %q", kept.Reason)
		}
		if kept.Path == data && strings.Contains(kept.Reason, "1 more files") {
			t.Errorf("the overflow reads %q", kept.Reason)
		}
	}
}

// --- helpers ---

// anInstallation is the state every purge starts from: a data directory with a
// database inside it, in a HOME of this test's own.
func anInstallation(t *testing.T) (home, data, database string) {
	t.Helper()
	home = t.TempDir()
	data = filepath.Join(home, ".roca")
	return home, data, touch(t, data, "roca.db")
}

func touch(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeDir(t *testing.T, parent, name string) string {
	t.Helper()
	path := filepath.Join(parent, name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
