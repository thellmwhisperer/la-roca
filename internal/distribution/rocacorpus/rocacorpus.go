package rocacorpus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/store/exactdedup"
	"github.com/thellmwhisperer/la-roca/pkg/ingestprovenance"
	sqlite "modernc.org/sqlite"
)

const (
	Name             = "roca-corpus"
	DatabaseFilename = "roca-corpus.db"
	// BundledSource is what the installer records for this package, and it is
	// what discovery reads to know the corpus attach alias is the kernel's own.
	BundledSource = plugin.BundledSource
	SchemaVersion = 5
	IndexVersion  = 3
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, BundleSpec())
}

// ApplySchema replays the whole declaration because every statement in it is
// guarded, so a version update over a database that already carries the harvest
// leaves its rows untouched.
func ApplySchema(path string) error {
	if err := prepareIngestProvenance(path); err != nil {
		return err
	}
	seals, err := snapshotMigrationSeals(path)
	if err != nil {
		return err
	}
	rewrote, err := applyStorageLaw(path, false)
	if err != nil {
		return err
	}
	if err := bundledplugin.ApplySchema(path, Name, schema, SchemaVersion, IndexVersion); err != nil {
		return err
	}
	if rewrote {
		if err := restoreMigrationSeals(path, seals); err != nil {
			return err
		}
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := exactdedup.EnsureGuards(context.Background(), db); err != nil {
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
			// A corpus that still has exact duplicates cannot take the unique hash
			// index. Version content is already gone; uniqueness waits on exact dedup.
			return nil
		}
		return err
	}
	return nil
}

// prepareIngestProvenance upgrades the corpus before the migration ledger is
// advanced. It retires the derived index before adding the one column CREATE IF
// NOT EXISTS cannot add to an existing table, then labels only derivable rows.
func prepareIngestProvenance(path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin bundled %s provenance migration: %w", Name, err)
	}
	defer tx.Rollback()
	ledgerPresent, err := tableExists(tx, "plugin_schema")
	if err != nil {
		return err
	}
	if ledgerPresent {
		var installedVersion int
		if err := tx.QueryRow(`SELECT schema_version FROM plugin_schema WHERE singleton = 1`).
			Scan(&installedVersion); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("inspect bundled %s schema version: %w", Name, err)
		}
		if installedVersion >= SchemaVersion {
			return nil
		}
	}
	tablePresent, err := tableExists(tx, "sessions")
	if err != nil {
		return err
	}
	columnPresent := false
	if tablePresent {
		columnPresent, err = columnExists(context.Background(), tx, "sessions", "source_surface")
		if err != nil {
			return err
		}
	}
	altered := tablePresent && !columnPresent
	archiveTablePresent, err := tableExists(tx, "session_versions")
	if err != nil {
		return err
	}
	archiveColumnPresent := false
	if archiveTablePresent {
		archiveColumnPresent, err = columnExists(context.Background(), tx,
			"session_versions", "source_surface")
		if err != nil {
			return err
		}
	}
	archiveAltered := archiveTablePresent && !archiveColumnPresent
	if altered || archiveAltered {
		var statements []string
		if altered {
			statements = append(statements,
				`DROP TRIGGER IF EXISTS sessions_ai`,
				`DROP TRIGGER IF EXISTS sessions_ad`,
				`DROP TRIGGER IF EXISTS sessions_au`,
				`DROP TABLE IF EXISTS sessions_fts`)
		}
		if archiveAltered {
			statements = append(statements, `DROP TABLE IF EXISTS session_versions_fts`)
		}
		for _, statement := range statements {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("retire the derived session index: %w", err)
			}
		}
	}
	if altered {
		if _, err := tx.Exec(`ALTER TABLE sessions ADD COLUMN source_surface TEXT`); err != nil {
			return fmt.Errorf("add sessions.source_surface: %w", err)
		}
	}
	if archiveAltered {
		if _, err := tx.Exec(`ALTER TABLE session_versions ADD COLUMN source_surface TEXT`); err != nil {
			return fmt.Errorf("add session_versions.source_surface: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bundled %s provenance migration: %w", Name, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("prepare bundled %s schema: %w", Name, err)
	}
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin bundled %s provenance backfill: %w", Name, err)
	}
	defer tx.Rollback()
	if altered {
		if _, err := tx.Exec(`INSERT INTO sessions_fts(sessions_fts) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("rebuild the derived session index: %w", err)
		}
	}
	var legacyGrokModels int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM exchanges e
		JOIN sessions s ON s.session_id = e.session_id
		WHERE e.model IN ('grok-4.6-build', 'grok-4.5-build')
		  AND (s.source_agent = 'grok' OR s.source_agent LIKE 'grok-%')`).
		Scan(&legacyGrokModels); err != nil {
		return fmt.Errorf("inspect historical Grok model labels: %w", err)
	}
	if legacyGrokModels > 0 {
		// An old frozen corpus can predate the FTS table itself. Seed the derived
		// index before the model/provider UPDATE fires its generic exchange trigger.
		if _, err := tx.Exec(`INSERT INTO exchanges_fts(exchanges_fts) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("rebuild the derived exchange index: %w", err)
		}
	}
	if err := ingestprovenance.Backfill(context.Background(), tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bundled %s provenance backfill: %w", Name, err)
	}
	return nil
}

func tableExists(db *sql.Tx, table string) (bool, error) {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		table).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect %s table: %w", table, err)
	}
	return count == 1, nil
}

func columnExists(ctx context.Context, db *sql.Tx, table, column string) (bool, error) {
	columns, err := bundledplugin.TableColumns(ctx, db, table)
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	return columns[column], nil
}
