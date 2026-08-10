package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hostile half of the purge. D-7's two halves protect each other, and both
// of them fail in the same direction when the inventory is incomplete: a path
// Roca created that nobody declared is left behind AND reported as somebody
// else's, which keeps the whole data directory alive and tells the operator to
// go and delete their own product by hand.

// A survivor that is on the inventory is Roca's, whatever kept it alive. Calling
// it "La Roca did not create it" is the second half of D-7 firing at the first
// half's own files, and the operator is sent to delete something the product
// already declared as its own.
//
// A data directory the operator locked down is one way to reach that state. A
// live process recreating its journal between the sweep and the listing is the
// other, and it is the one that happens on a machine where `roca mcp serve` is still
// running while the purge goes past.
func TestAnOwnedPathIsNeverReportedAsSomebodyElsesFile(t *testing.T) {
	data := t.TempDir()
	db := filepath.Join(data, "roca.db")
	write(t, db, "the corpus")
	if err := os.Chmod(data, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(data, 0o700) })

	report := Plan{Owned: []string{db}, DataDir: data}.Apply()
	if len(report.Deleted) > 0 {
		t.Skip("this filesystem let a read-only directory be emptied")
	}
	for _, kept := range report.Kept {
		if kept.Path != db {
			continue
		}
		if strings.Contains(kept.Reason, "did not create it") {
			t.Fatalf("the purge calls its own database somebody else's: %q", kept.Reason)
		}
	}
}

// The journals a live database writes are Roca's, and a purge that finds them
// back after its own sweep says so and deletes them on the next run.
//
// Unlinking a file another process has open succeeds on this platform, so the
// purge does not fail: what it has to get right is the second run, because a
// machine where `roca mcp serve` was alive during the first one is exactly the
// half-purged machine the command exists to converge over.
func TestAPurgeConvergesOverAJournalThatCameBack(t *testing.T) {
	data := t.TempDir()
	db := filepath.Join(data, "roca.db")
	write(t, db, "the corpus")
	write(t, db+"-wal", "the write-ahead log")

	held, err := os.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	plan := Plan{Owned: []string{db, db + "-wal", db + "-shm"}, DataDir: data}
	if report := plan.Apply(); !report.Purged {
		t.Fatalf("the purge failed with the database open: %v", report.Errors)
	}

	// What the process that still had it open writes next: SQLite writes its
	// journal back beside the database it is holding. The half-purged machine
	// the second run has to converge over is exactly this one.
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, db+"-wal", "written after the purge went past")

	second := plan.Apply()
	if !second.Purged {
		t.Fatalf("the second purge failed: %v", second.Errors)
	}
	if _, err := os.Stat(db + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("the journal that came back survived the second purge: %v", second.Deleted)
	}
	for _, kept := range second.Kept {
		t.Errorf("the second purge kept %s: %s", kept.Path, kept.Reason)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Error("the data directory survived a purge that had nothing left to keep")
	}
}

// --- helpers ---

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
