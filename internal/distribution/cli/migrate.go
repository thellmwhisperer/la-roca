package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/datasplit"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func migrateCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use: "migrate", Short: "Resume and verify DATA SPLIT custody migration",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if status, _ := cmd.Flags().GetBool("status"); status {
				// A status read never writes, so it is exactly the answer a
				// read-only invocation can give (issue #455).
				return migrateStatus(env, cmd)
			}
			if env.forceReadOnly || config.ReadOnly(os.Getenv(config.EnvReadOnly)) {
				return fmt.Errorf("roca migrate requires writable databases")
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			root := pluginRoot(paths)
			for _, ensure := range []func(string, string, string) (plugininstall.Result, error){
				rocaops.Ensure, rocacorpus.Ensure, rocacron.Ensure,
			} {
				if _, err := ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
					return err
				}
			}
			options := migrationHubOptions(paths)
			options.Progress = func(step datasplit.HubProgress) error {
				return printMigrateProgress(env, step)
			}
			report, err := datasplit.Migrate(cmd.Context(), options)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{"verified": report.Ready})
			}
			env.print("migration: verified")
			return nil
		},
	}
	command.Flags().Bool("status", false,
		"report the DATA SPLIT custody ledger read-only and exit")
	return command
}

// printMigrateProgress writes one stage or batch line. Text mode answers on
// stdout; --json keeps stdout a single machine-readable document and mirrors
// progress on stderr instead, so the final JSON stays the last thing there.
func printMigrateProgress(env *cliEnv, step datasplit.HubProgress) error {
	line := migrateProgressLine(step)
	if line == "" {
		return nil
	}
	if env.json {
		fmt.Fprintln(env.errOut, line)
		return nil
	}
	env.print("%s", line)
	return nil
}

func migrateProgressLine(step datasplit.HubProgress) string {
	line := "stage=" + step.Stage
	if step.Source != "" {
		line += " source=" + step.Source
	}
	if step.Batch > 0 {
		line += fmt.Sprintf(" batch=%d/%d rows=%d", step.Batch, step.Batches, step.Rows)
		return line
	}
	if step.Detail != "" {
		line += " detail=" + step.Detail
	}
	return line
}

func migrationHubOptions(paths config.Paths) datasplit.HubOptions {
	root := pluginRoot(paths)
	return datasplit.HubOptions{
		CoreDatabase:   paths.DB,
		OpsDatabase:    filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename),
		CorpusDatabase: filepath.Join(root, rocacorpus.Name, rocacorpus.DatabaseFilename),
		CronDatabase:   filepath.Join(root, rocacron.Name, rocacron.DatabaseFilename),
		SnapshotDir:    filepath.Join(paths.Backups, "data-split"),
		LockPath:       logfile.New(filepath.Dir(paths.DB)).LockPath(),
	}
}

// migrateLedger names one plugin database whose custody ledger `roca migrate
// --status` reports read-only. The status never opens a writable connection
// and never takes the migration lock, so it is safe beside a running migrate.
var migrateLedgers = []struct{ plugin, filename string }{
	{rocaops.Name, rocaops.DatabaseFilename},
	{rocacorpus.Name, rocacorpus.DatabaseFilename},
	{rocacron.Name, rocacron.DatabaseFilename},
}

type migrateLedgerStatus struct {
	Plugin        string                     `json:"plugin"`
	Present       bool                       `json:"present"`
	SchemaVersion int                        `json:"schema_version"`
	IndexVersion  int                        `json:"index_version"`
	Migrations    []migrateMigrationSnapshot `json:"migrations"`
}

type migrateMigrationSnapshot struct {
	Migration        string `json:"migration"`
	DestinationTable string `json:"destination_table"`
	State            string `json:"state"`
	Memberships      int    `json:"memberships"`
	Batches          int    `json:"batches"`
}

// migrateStatus reports every plugin custody ledger without writing anything:
// each database opens read-only, and the migration lock stays untouched, so an
// operator can tell "working" from "stuck" while a migrate runs (issue #455).
func migrateStatus(env *cliEnv, cmd *cobra.Command) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	root := pluginRoot(paths)
	reports := make([]migrateLedgerStatus, 0, len(migrateLedgers))
	for _, ledger := range migrateLedgers {
		status, err := migrateLedgerReport(cmd.Context(),
			filepath.Join(root, ledger.plugin, ledger.filename))
		if err != nil {
			return err
		}
		status.Plugin = ledger.plugin
		reports = append(reports, status)
	}
	if env.json {
		return env.printJSON(map[string]any{"ledgers": reports})
	}
	for _, report := range reports {
		if !report.Present {
			env.print("%s: absent", report.Plugin)
			continue
		}
		env.print("%s: schema %d index %d", report.Plugin, report.SchemaVersion, report.IndexVersion)
		for _, migration := range report.Migrations {
			env.print("  %s state=%s destination=%s memberships=%d batches=%d",
				migration.Migration, migration.State, migration.DestinationTable,
				migration.Memberships, migration.Batches)
		}
	}
	return nil
}

func migrateLedgerReport(ctx context.Context, path string) (migrateLedgerStatus, error) {
	status := migrateLedgerStatus{Migrations: []migrateMigrationSnapshot{}}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return status, nil
	} else if err != nil {
		return status, fmt.Errorf("inspect %s custody ledger: %w", path, err)
	}
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return status, fmt.Errorf("open %s custody ledger read-only: %w", path, err)
	}
	defer db.Close()
	identity, err := migrationledger.Inspect(ctx, db)
	if err != nil {
		return status, fmt.Errorf("read %s custody ledger identity: %w", path, err)
	}
	if identity.Plugin == "" {
		return status, nil
	}
	status.Present = true
	status.SchemaVersion = identity.SchemaVersion
	status.IndexVersion = identity.IndexVersion
	migrations, err := migrationledger.ListMigrations(ctx, db)
	if err != nil {
		return status, fmt.Errorf("read %s custody migrations: %w", path, err)
	}
	for _, migration := range migrations {
		entry := migrateMigrationSnapshot{
			Migration:        migration.Name,
			DestinationTable: migration.DestinationTable,
			State:            string(migration.State),
		}
		if entry.Memberships, err = countRows(ctx, db, `SELECT COUNT(*) FROM custody_memberships
			WHERE migration = ?`, migration.Name); err != nil {
			return status, fmt.Errorf("count %s memberships: %w", migration.Name, err)
		}
		if entry.Batches, err = countRows(ctx, db, `SELECT COUNT(*) FROM migration_batches
			WHERE migration = ?`, migration.Name); err != nil {
			return status, fmt.Errorf("count %s batches: %w", migration.Name, err)
		}
		status.Migrations = append(status.Migrations, entry)
	}
	return status, nil
}

func countRows(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
