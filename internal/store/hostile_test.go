package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The seam every operator who passes `--db-path` can walk into: the path is
// somewhere they cannot write.
//
// SQLite answers SQLITE_CANTOPEN and the driver renders its extended code as
// **"out of memory"**, which is a true sentence about a different machine. An
// operator reading it looks at their RAM, and what is wrong is a directory
// permission. It is the D-3 lesson at the very first thing the product does.

// A database under a directory that cannot be written to says so, and says what
// to do about it.
func TestADatabaseUnderADirectoryYouCannotWriteToSaysThat(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(directory, 0o700) })

	db, err := Open(filepath.Join(directory, "roca.db"))
	if err == nil {
		db.Close()
		t.Skip("this filesystem let a read-only directory be written to")
	}
	if strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("a permission problem is reported as a memory one: %v", err)
	}
	if !strings.Contains(err.Error(), directory) {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
	if !strings.Contains(err.Error(), "--db-path") {
		t.Errorf("the refusal names no way out: %v", err)
	}
}

// A database file the operator can read and not write is the other half, and it
// is a different remedy: the directory is fine and the file is the problem.
func TestADatabaseFileYouCannotWriteToNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err == nil {
		reopened.Close()
		t.Skip("this filesystem let a read-only file be written to")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the refusal does not name the database: %v", err)
	}
	if strings.Contains(err.Error(), "out of memory") {
		t.Errorf("a permission problem is reported as a memory one: %v", err)
	}
}

// And what has to keep working: a path whose directory does not exist yet is not
// a permission problem and must not be reported as one.
func TestAMissingDirectoryIsNotReportedAsAPermissionProblem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not", "there", "roca.db")

	db, err := Open(path)
	if err == nil {
		db.Close()
		t.Fatal("a database was opened under a directory that does not exist")
	}
	if strings.Contains(err.Error(), "permission") {
		t.Errorf("a missing directory is reported as a permission problem: %v", err)
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the refusal does not say the directory is missing: %v", err)
	}
}
