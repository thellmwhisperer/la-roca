package bundledplugin_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

func TestOpenDatabaseSharesBusyTimeoutAndReadOnlyMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin.db")
	writable, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec("CREATE TABLE records (id INTEGER PRIMARY KEY)"); err != nil {
		writable.Close()
		t.Fatal(err)
	}
	assertBusyTimeout(t, writable)
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	readonly, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	assertBusyTimeout(t, readonly)
	if _, err := readonly.Exec("INSERT INTO records DEFAULT VALUES"); err == nil {
		t.Fatal("read-only bundled plugin database accepted a write")
	}
}

func assertBusyTimeout(t *testing.T, db *sql.DB) {
	t.Helper()
	var milliseconds int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&milliseconds); err != nil {
		t.Fatal(err)
	}
	if milliseconds != 15_000 {
		t.Fatalf("busy timeout = %dms, want 15000ms", milliseconds)
	}
}
