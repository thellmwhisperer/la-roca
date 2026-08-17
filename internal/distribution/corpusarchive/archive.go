// Package corpusarchive merges frozen core and plugin session archives into
// corpus-owned shadow versions without changing the serving route. A source is
// accepted only as a complete standalone clone, which is what `VACUUM INTO`
// produces and what a file copy of a live database is not.
package corpusarchive

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	_ "modernc.org/sqlite"
)

const DefaultBatchSize = 1000

const (
	sqliteHeaderMagic   = "SQLite format 3\x00"
	sqliteHeaderLength  = 100
	sqliteReadVersion   = 18
	sqliteWriteVersion  = 19
	sqliteLegacyJournal = 1
)

type Source struct {
	Database       string
	Path           string
	SnapshotDigest string
	ExistingCorpus bool
}

type Options struct {
	BatchSize int
}

type FamilyReport struct {
	Identities    int64 `json:"identities"`
	PhysicalRows  int64 `json:"physical_rows"`
	ExactAliases  int64 `json:"exact_aliases"`
	DivergentKeys int64 `json:"divergent_keys"`
	FTSRows       int64 `json:"fts_rows,omitempty"`
}

type SourceReport struct {
	Database       string           `json:"database"`
	SnapshotDigest string           `json:"snapshot_digest"`
	ExpectedRows   map[string]int64 `json:"expected_rows"`
}

type Report struct {
	Sources            []SourceReport          `json:"sources"`
	Families           map[string]FamilyReport `json:"families"`
	Reconciliation     ReconciliationReport    `json:"reconciliation"`
	VerificationDigest string                  `json:"verification_digest"`
}

type preparedSource struct {
	Source
	db *sql.DB
}

type archiveRun struct {
	sources     []preparedSource
	destination *sql.DB
	batchSize   int
}

// CutoverEligible reports whether all five DATA-3 archive families have a
// verified population in the corpus destination. It is a cheap readiness
// probe for DATA-6 and never opens a source snapshot.
func CutoverEligible(ctx context.Context, destinationPath string) (bool, error) {
	destination, err := bundledplugin.OpenDatabase(destinationPath, true)
	if err != nil {
		return false, err
	}
	defer destination.Close()
	for _, table := range archiveSourceTables {
		ready, err := migrationledger.MigrationCutoverEligible(ctx, destination, table.migration)
		if err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}

func Merge(ctx context.Context, destinationPath string, sources []Source, options Options) (Report, error) {
	run, err := openArchiveRun(ctx, destinationPath, sources, options, true)
	if err != nil {
		return Report{}, err
	}
	defer run.close()
	states, err := prepareArchiveMigrations(ctx, run.destination)
	if err != nil {
		return Report{}, err
	}
	verified := archiveVerified(states)
	if err := validateRecordedSources(ctx, run.destination, run.sources, run.batchSize, verified); err != nil {
		return Report{}, err
	}
	if !verified {
		for _, source := range run.sources {
			if err := importSource(ctx, run.destination, source, run.batchSize); err != nil {
				return Report{}, err
			}
		}
		if err := rebuildArchiveFTS(ctx, run.destination); err != nil {
			return Report{}, err
		}
	}
	report, err := buildReport(ctx, run.destination, run.sources)
	if err != nil {
		return report, err
	}
	digest, err := reportDigest(report)
	if err != nil {
		return Report{}, err
	}
	if err := recordArchiveVerification(ctx, run.destination, states, digest); err != nil {
		return Report{}, err
	}
	report.VerificationDigest = digest
	return report, nil
}

// Verify reproduces DATA-3's frozen-source reconciliation without importing
// rows. It succeeds only when the recorded migrations, every custody table,
// and every source session still reproduce the verification digest that Merge
// sealed.
func Verify(ctx context.Context, destinationPath string, sources []Source, options Options) (Report, error) {
	run, err := openArchiveRun(ctx, destinationPath, sources, options, false)
	if err != nil {
		return Report{}, err
	}
	defer run.close()
	if err := validateRecordedSources(ctx, run.destination, run.sources, run.batchSize, true); err != nil {
		return Report{}, err
	}
	report, err := buildReport(ctx, run.destination, run.sources)
	if err != nil {
		return report, err
	}
	digest, err := reportDigest(report)
	if err != nil {
		return report, err
	}
	for _, table := range archiveSourceTables {
		state, inspectErr := migrationledger.InspectMigration(ctx, run.destination, table.migration)
		if inspectErr != nil {
			return report, inspectErr
		}
		if state.State != migrationledger.StateVerified {
			report.Reconciliation.Status = ReconciliationRed
			return report, fmt.Errorf("DATA-3 migration %q is %q, want verified",
				table.migration, state.State)
		}
		if state.VerificationDigest != digest {
			report.Reconciliation.Status = ReconciliationRed
			return report, fmt.Errorf("DATA-3 migration %q recorded digest %s, reproduced %s",
				table.migration, state.VerificationDigest, digest)
		}
	}
	report.VerificationDigest = digest
	return report, nil
}

func openArchiveRun(ctx context.Context, destinationPath string, sources []Source,
	options Options, applySchema bool,
) (*archiveRun, error) {
	prepared, err := prepareSources(ctx, destinationPath, sources)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*archiveRun, error) {
		closeSources(prepared)
		return nil, err
	}
	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	if applySchema {
		if err := rocacorpus.ApplySchema(destinationPath); err != nil {
			return fail(err)
		}
	}
	destination, err := bundledplugin.OpenDatabase(destinationPath, false)
	if err != nil {
		return fail(err)
	}
	destination.SetMaxOpenConns(1)
	return &archiveRun{sources: prepared, destination: destination, batchSize: batchSize}, nil
}

func (run *archiveRun) close() {
	_ = run.destination.Close()
	closeSources(run.sources)
}

// prepareArchiveMigrations declares one named migration per family and reports
// what each of them already recorded. Declaring is idempotent and leaves a
// family that already carried rows exactly where it was.
func prepareArchiveMigrations(ctx context.Context, destination *sql.DB,
) ([]migrationledger.MigrationSnapshot, error) {
	states := make([]migrationledger.MigrationSnapshot, 0, len(archiveSourceTables))
	for _, table := range archiveSourceTables {
		if err := migrationledger.PrepareMigration(ctx, destination, migrationledger.Migration{
			Name: table.migration, DestinationTable: table.destinationTable,
		}); err != nil {
			return nil, err
		}
		state, err := migrationledger.InspectMigration(ctx, destination, table.migration)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

// archiveVerified reports the archive as verified only when every family is.
// The report and its digest cover all five together, so a run that sealed some
// of them and stopped still owes the whole merge again.
func archiveVerified(states []migrationledger.MigrationSnapshot) bool {
	for _, state := range states {
		if state.State != migrationledger.StateVerified {
			return false
		}
	}
	return true
}

// recordArchiveVerification seals the families that are not sealed yet and
// holds the ones that are to the digest they recorded, so a replay of a
// verified archive that no longer reproduces its report is refused rather than
// re-sealed.
func recordArchiveVerification(ctx context.Context, destination *sql.DB,
	states []migrationledger.MigrationSnapshot, digest string,
) error {
	for _, state := range states {
		if state.State == migrationledger.StateVerified {
			if state.VerificationDigest != digest {
				return fmt.Errorf("verified corpus archive digest is %s, rebuilt report is %s",
					state.VerificationDigest, digest)
			}
			continue
		}
		if err := migrationledger.VerifyMigration(ctx, destination, state.Name, digest); err != nil {
			return err
		}
	}
	return nil
}

func prepareSources(ctx context.Context, destinationPath string, sources []Source) ([]preparedSource, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("corpus archive merge needs at least one frozen source")
	}
	destination, err := filepath.Abs(destinationPath)
	if err != nil {
		return nil, fmt.Errorf("resolve corpus archive destination: %w", err)
	}
	seen := make(map[string]bool, len(sources))
	existingCorpus := 0
	prepared := make([]preparedSource, 0, len(sources))
	fail := func(err error) ([]preparedSource, error) {
		closeSources(prepared)
		return nil, err
	}
	for _, source := range sources {
		if source.Database == "" || source.Path == "" || source.SnapshotDigest == "" {
			return fail(fmt.Errorf("every corpus source needs a database label, frozen path, and snapshot digest"))
		}
		if seen[source.Database] {
			return fail(fmt.Errorf("duplicate corpus source database %q", source.Database))
		}
		seen[source.Database] = true
		if source.ExistingCorpus {
			existingCorpus++
		}
		absolute, err := filepath.Abs(source.Path)
		if err != nil {
			return fail(fmt.Errorf("resolve corpus source %q: %w", source.Path, err))
		}
		if absolute == destination {
			return fail(fmt.Errorf("corpus source %q is the writable destination, not a frozen clone", absolute))
		}
		actualDigest, err := SnapshotDigest(absolute)
		if err != nil {
			return fail(err)
		}
		if actualDigest != source.SnapshotDigest {
			return fail(fmt.Errorf("frozen corpus source %q digest is %s, want %s",
				absolute, actualDigest, source.SnapshotDigest))
		}
		source.Path = absolute
		db, err := openFrozenSource(absolute)
		if err != nil {
			return fail(err)
		}
		if err := verifyFrozenSource(ctx, db, source.Database); err != nil {
			db.Close()
			return fail(err)
		}
		prepared = append(prepared, preparedSource{Source: source, db: db})
	}
	if existingCorpus != 1 {
		return fail(fmt.Errorf("corpus archive merge needs exactly one existing-corpus source, got %d", existingCorpus))
	}
	sort.SliceStable(prepared, func(left, right int) bool {
		if prepared[left].ExistingCorpus != prepared[right].ExistingCorpus {
			return prepared[left].ExistingCorpus
		}
		return prepared[left].Database < prepared[right].Database
	})
	return prepared, nil
}

// vacuumRemedy is the one accepted way to produce a frozen source. `VACUUM
// INTO` writes a complete standalone database, folding whatever a write-ahead
// log still held into the clone, which a file copy leaves behind.
const vacuumRemedy = "produce it with SQLite `VACUUM INTO`, which writes one complete standalone clone"

// acceptStandaloneClone keeps the immutable read honest. `immutable=1` reads
// the main database file alone, so a copy taken while a log held committed
// pages is read as its pre-log prefix, and every count below agrees with that
// truncated view: the completeness has to be established before the read, from
// how the clone was made.
func acceptStandaloneClone(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Stat(path + suffix)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect frozen corpus source %q: %w", path+suffix, err)
		}
		if info.Size() > 0 {
			return fmt.Errorf("frozen corpus source %q carries a non-empty %s sidecar, "+
				"so it is not a standalone clone: %s", path, suffix, vacuumRemedy)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open frozen corpus source %q: %w", path, err)
	}
	defer file.Close()
	header := make([]byte, sqliteHeaderLength)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("read the frozen corpus source header of %q: %w", path, err)
	}
	if !bytes.HasPrefix(header, []byte(sqliteHeaderMagic)) {
		return fmt.Errorf("frozen corpus source %q is not a SQLite database", path)
	}
	if header[sqliteReadVersion] != sqliteLegacyJournal ||
		header[sqliteWriteVersion] != sqliteLegacyJournal {
		return fmt.Errorf("frozen corpus source %q is a raw copy of a write-ahead-log database, "+
			"whose committed pages can still live in a sidecar this read never sees: %s",
			path, vacuumRemedy)
	}
	return nil
}

func openFrozenSource(path string) (*sql.DB, error) {
	if err := acceptStandaloneClone(path); err != nil {
		return nil, err
	}
	query := url.Values{"mode": {"ro"}, "immutable": {"1"}}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open frozen corpus source %q: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open frozen corpus source %q: %w", path, err)
	}
	return db, nil
}

func verifyFrozenSource(ctx context.Context, db *sql.DB, label string) error {
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify frozen corpus source %q: %w", label, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("frozen corpus source %q failed integrity_check: %s", label, integrity)
	}
	for _, table := range archiveSourceTables {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?`, table.sourceTable).Scan(&exists); err != nil {
			return fmt.Errorf("inspect frozen corpus source %q: %w", label, err)
		}
		if exists != 1 {
			return fmt.Errorf("frozen corpus source %q has no %s table", label, table.sourceTable)
		}
	}
	return nil
}

func closeSources(sources []preparedSource) {
	for _, source := range sources {
		_ = source.db.Close()
	}
}

func validateRecordedSources(ctx context.Context, destination *sql.DB,
	sources []preparedSource, batchSize int, requireRecorded bool,
) error {
	for _, source := range sources {
		var digest string
		var existingCorpus, recordedBatchSize int
		err := destination.QueryRowContext(ctx, `SELECT snapshot_digest, destination_source, batch_size
			FROM corpus_source_snapshots WHERE source_database = ?`, source.Database).
			Scan(&digest, &existingCorpus, &recordedBatchSize)
		if errors.Is(err, sql.ErrNoRows) {
			if requireRecorded {
				return fmt.Errorf("corpus source %q is absent from the verified archive", source.Database)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect recorded corpus source %q: %w", source.Database, err)
		}
		if digest != source.SnapshotDigest || (existingCorpus == 1) != source.ExistingCorpus ||
			recordedBatchSize != batchSize {
			return fmt.Errorf("corpus source %q does not match the snapshot already being merged", source.Database)
		}
	}
	return nil
}
