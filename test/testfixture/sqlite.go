package testfixture

import (
	"database/sql"
	"testing"
)

// OpenSQLite opens the SQLite file at path and fails the test on error.
func OpenSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
