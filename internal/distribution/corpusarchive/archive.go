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
	VerificationDigest string                  `json:"verification_digest"`
}

type preparedSource struct {
	Source
	db *sql.DB
}

func Merge(ctx context.Context, destinationPath string, sources []Source, options Options) (Report, error) {
	prepared, err := prepareSources(ctx, destinationPath, sources)
	if err != nil {
		return Report{}, err
	}
	defer closeSources(prepared)
	if options.BatchSize <= 0 {
		options.BatchSize = DefaultBatchSize
	}
	if err := rocacorpus.ApplySchema(destinationPath); err != nil {
		return Report{}, err
	}
	destination, err := bundledplugin.OpenDatabase(destinationPath, false)
	if err != nil {
		return Report{}, err
	}
	defer destination.Close()
	destination.SetMaxOpenConns(1)
	state, err := migrationledger.Inspect(ctx, destination)
	if err != nil {
		return Report{}, err
	}
	if err := validateRecordedSources(ctx, destination, prepared, options.BatchSize,
		state.State == migrationledger.StateVerified); err != nil {
		return Report{}, err
	}
	if state.State != migrationledger.StateVerified {
		for _, source := range prepared {
			if err := importSource(ctx, destination, source, options.BatchSize); err != nil {
				return Report{}, err
			}
		}
		if err := rebuildArchiveFTS(ctx, destination); err != nil {
			return Report{}, err
		}
	}
	report, err := verifyArchive(ctx, destination)
	if err != nil {
		return Report{}, err
	}
	digest, err := reportDigest(report)
	if err != nil {
		return Report{}, err
	}
	if state.State == migrationledger.StateVerified {
		if state.VerificationDigest != digest {
			return Report{}, fmt.Errorf("verified corpus archive digest is %s, rebuilt report is %s",
				state.VerificationDigest, digest)
		}
	} else if err := migrationledger.Verify(ctx, destination, digest); err != nil {
		return Report{}, err
	}
	report.VerificationDigest = digest
	return report, nil
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
