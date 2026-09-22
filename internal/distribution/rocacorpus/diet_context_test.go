package rocacorpus

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
)

func TestStorageLawRewriteHonorsCancellation(t *testing.T) {
	db, path := openSchemaDB(t)
	addTitleColumnAndClose(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := applyStorageLaw(ctx, path, false); err == nil {
		t.Fatal("storage-law rewrite ignored cancellation")
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	present, err := columnExistsDB(context.Background(), db, "session_versions", "title")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("canceled storage-law rewrite changed the database")
	}
}

func TestVacuumHonorsCancellation(t *testing.T) {
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := vacuumDatabase(ctx, path); err == nil {
		t.Fatal("VACUUM ignored cancellation")
	}
}

func TestApplySchemaKeepsPriorGuardWhenExactDuplicatesRemain(t *testing.T) {
	for _, tc := range sessionClonePlaceCases() {
		t.Run(tc.name, func(t *testing.T) {
			db, path := openSchemaDB(t)
			tc.plant(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if err := ApplySchema(path); err != nil {
				t.Fatalf("ApplySchema = %v", err)
			}
			if err := ApplySchema(path); err != nil {
				t.Fatalf("ApplySchema second pass = %v", err)
			}
			assertPlacedSessionClones(t, path, tc)
		})
	}
}

func TestEnsureAllPlacesCorpusWhenExactSessionDuplicatesRemain(t *testing.T) {
	for _, tc := range sessionClonePlaceCases() {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(t.TempDir(), "bin")
			spec := BundleSpec()
			if _, err := bundledplugin.Ensure(root, bin, "1.86.6", spec); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, Name, DatabaseFilename)
			db, err := bundledplugin.OpenDatabase(path, false)
			if err != nil {
				t.Fatal(err)
			}
			tc.plant(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if _, err := bundledplugin.EnsureAll(root, bin, "1.87.1", spec); err != nil {
				t.Fatalf("EnsureAll = %v", err)
			}
			if _, err := bundledplugin.EnsureAll(root, bin, "1.87.1", spec); err != nil {
				t.Fatalf("EnsureAll second pass = %v", err)
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(root, Name))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Version != "1.87.1" {
				t.Fatalf("placed version = %q, want 1.87.1", manifest.Version)
			}
			assertPlacedSessionClones(t, path, tc)
		})
	}
}

type sessionClonePlaceCase struct {
	name          string
	plant         func(*testing.T, *sql.DB)
	wantHashGuard bool
	wantSessions  int
	wantSurfaces  map[string]string
	wantMachines  map[string]sql.NullString
}

func sessionClonePlaceCases() []sessionClonePlaceCase {
	return []sessionClonePlaceCase{
		{
			name:          "prior non-unique leftover clones",
			plant:         plantExactSessionDuplicates,
			wantHashGuard: false,
			wantSessions:  2,
		},
		{
			name:          "unique guard plus unlabeled clone of a labeled session",
			plant:         plantUniqueGuardSurfaceClones,
			wantHashGuard: true,
			wantSessions:  3,
			wantSurfaces: map[string]string{
				"labeled":   ingestprovenance.ClaudeCode,
				"unlabeled": "",
				"fillable":  ingestprovenance.ClaudeCode,
			},
		},
		{
			name:          "unique guard plus unlabeled machine clone",
			plant:         plantUniqueGuardMachineClones,
			wantHashGuard: true,
			wantSessions:  2,
			wantSurfaces: map[string]string{
				"labeled-machine":   ingestprovenance.ClaudeCode,
				"unlabeled-machine": ingestprovenance.ClaudeCode,
			},
			wantMachines: map[string]sql.NullString{
				"labeled-machine":   {String: localMachine(), Valid: true},
				"unlabeled-machine": {},
			},
		},
	}
}

func assertPlacedSessionClones(t *testing.T, path string, tc sessionClonePlaceCase) {
	t.Helper()
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master
		WHERE type = 'index' AND name = 'idx_sessions_exact_payload'`).Scan(&indexSQL); err != nil {
		t.Fatal(err)
	}
	hasHash := strings.Contains(strings.ToLower(indexSQL), "roca_payload_hash")
	if hasHash != tc.wantHashGuard {
		t.Fatalf("hash guard present = %v, want %v: %s", hasHash, tc.wantHashGuard, indexSQL)
	}
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != tc.wantSessions {
		t.Fatalf("session count = %d, want %d", sessions, tc.wantSessions)
	}
	for sessionID, want := range tc.wantSurfaces {
		var got sql.NullString
		if err := db.QueryRow(`SELECT source_surface FROM sessions WHERE session_id = ?`,
			sessionID).Scan(&got); err != nil {
			t.Fatalf("read %s source_surface: %v", sessionID, err)
		}
		if got.String != want {
			t.Fatalf("%s source_surface = %q, want %q", sessionID, got.String, want)
		}
	}
	for sessionID, want := range tc.wantMachines {
		var got sql.NullString
		if err := db.QueryRow(`SELECT machine FROM sessions WHERE session_id = ?`,
			sessionID).Scan(&got); err != nil {
			t.Fatalf("read %s machine: %v", sessionID, err)
		}
		if got != want {
			t.Fatalf("%s machine = %+v, want %+v", sessionID, got, want)
		}
	}
}

func plantExactSessionDuplicates(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP INDEX idx_sessions_exact_payload`,
		`CREATE INDEX idx_sessions_exact_payload ON sessions(source_agent, title, metadata)`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		 VALUES ('duplicate-a', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata)
		 VALUES ('duplicate-b', 'fixture', 'same', '2026-08-16T10:00:00Z', '{}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}
}

func plantUniqueGuardSurfaceClones(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
		 VALUES ('labeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', 'Claude Code')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
		 VALUES ('unlabeled', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', '')`,
		`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
		 VALUES ('fillable', 'claude', 'other', '2026-08-16T10:00:00Z', '{}', '')`,
		`UPDATE plugin_schema SET schema_version = schema_version - 1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("fixture %q: %v", statement, err)
		}
	}
}

func plantUniqueGuardMachineClones(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface, machine)
		 VALUES ('labeled-machine', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', 'Claude Code', ?)`,
		localMachine()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(session_id, source_agent, title, started_at, metadata, source_surface)
		 VALUES ('unlabeled-machine', 'claude', 'same', '2026-08-16T10:00:00Z', '{}', 'Claude Code')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin_schema SET schema_version = schema_version - 1`); err != nil {
		t.Fatal(err)
	}
}

func localMachine() string {
	machine, err := os.Hostname()
	if err != nil || strings.TrimSpace(machine) == "" {
		return "local"
	}
	return strings.TrimSpace(machine)
}

func TestCanceledCompactRestoresSchemaAfterCommittedRewrite(t *testing.T) {
	db, path := openSchemaDB(t)
	addTitleColumnAndClose(t, db)
	if rewrote, err := applyStorageLaw(context.Background(), path, false); err != nil {
		t.Fatal(err)
	} else if !rewrote {
		t.Fatal("storage-law fixture did not rewrite")
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'view' AND name = 'exchange_version_memberships'`).Scan(&missing); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if missing != 0 {
		t.Fatal("storage-law fixture unexpectedly retained the derived view")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := restoreCompactSchema(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("restoreCompactSchema error = %v, want context canceled", err)
	}
	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var restored int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'view' AND name = 'exchange_version_memberships'`).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored != 1 {
		t.Fatal("canceled compact left the derived schema unrestored")
	}
	guards, err := exactdedup.GuardsInstalled(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if !guards {
		t.Fatal("canceled compact left hash guards unrestored")
	}
}

// openSchemaDB applies the corpus schema to a fresh database and opens it for
// writing, returning the handle and the file path for tests that reopen it.
func openSchemaDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

// addTitleColumnAndClose adds the pre-storage-law title column and closes the
// database, the shared fixture of the cancellation tests.
func addTitleColumnAndClose(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE session_versions ADD COLUMN title TEXT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
