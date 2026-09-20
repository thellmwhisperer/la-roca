package rocaops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const defaultMemoryBatchSize = 250

// memoryOrphansQuery counts physical records no committed identity represents.
// destination_key compares as text against CAST(records.id AS TEXT) — exactly
// the form its writers produce — so the correlated NOT EXISTS seeks the
// custody_memberships(migration, destination_key) index. Wrapping the column
// in CAST instead defeated every index and swept every membership of the
// migration once per record: the quadratic verify behind issue #455.
const memoryOrphansQuery = `SELECT COUNT(*) FROM memory_records AS records
WHERE NOT EXISTS (SELECT 1 FROM custody_memberships AS memberships
	WHERE memberships.migration = ?
	  AND memberships.destination_key = CAST(records.id AS TEXT))`

// memoryAliasQuery finds a physical record an exact digest from another source
// already aliases. The same text-for-text destination_key comparison keeps
// both its membership probes on the destination index (issue #455).
const memoryAliasQuery = `SELECT records.id
FROM memory_records AS records
JOIN custody_memberships AS represented
  ON represented.migration = ?
 AND represented.destination_key = CAST(records.id AS TEXT)
WHERE records.canonical_digest = ?
  AND represented.source_database <> ?
  AND NOT EXISTS (
    SELECT 1 FROM custody_memberships AS same_source
    WHERE same_source.migration = ?
      AND same_source.destination_key = CAST(records.id AS TEXT)
      AND same_source.source_database = ?)
ORDER BY records.id`

const (
	coreMemorySource   = "core"
	corpusMemorySource = "plugin:roca-corpus"
	opsMemorySource    = "plugin:roca-ops"
)

// memoryCustodyMigration is DATA-2's stable name in the ops ledger. Every read
// and write this engine makes is keyed by it, so a migration another rung hosts
// in the same database neither counts toward nor overwrites this one.
const (
	memoryCustodyMigration   = "data2-memory-custody"
	memoryCustodyDestination = "memory_records"
)

// MemoryCustodyOptions names the three databases DATA-2 freezes and the ops
// database that receives their memory identities. SnapshotDir is intentionally
// explicit: callers keep the verified VACUUM copies beside their other Roca
// backups, never in a process-global temporary directory.
type MemoryCustodyOptions struct {
	CorePath    string
	CorpusPath  string
	OpsPath     string
	SnapshotDir string
	LockPath    string
	BatchSize   int
	AfterBatch  func(MemoryBatch) error
	// Progress, when set, observes every stage of a custody run: which
	// snapshot is being frozen or reused, when the import, FTS rebuild, and
	// verify stages begin. It is how a driver prints the stage lines an
	// operator needs to tell "working" from "stuck" (issue #455).
	Progress func(MemoryCustodyProgress) error
}

type MemoryBatch struct {
	ID             string
	SourceDatabase string
	RowCount       int
	HighWaterMark  string
	// Batch counts from one within the committing source's import stage,
	// and Batches is how many that stage planned, so a driver can print
	// batch/total progress.
	Batch   int
	Batches int
}

// MemoryCustodyStage names one phase of a DATA-2 custody run.
type MemoryCustodyStage string

const (
	StageSnapshot   MemoryCustodyStage = "snapshot"
	StageImport     MemoryCustodyStage = "import"
	StageFTSRebuild MemoryCustodyStage = "fts-rebuild"
	StageVerify     MemoryCustodyStage = "verify"
)

// MemoryCustodyProgress is one observable step of a custody run. Source names
// the database the step concerns and is empty for migration-wide stages; Detail
// carries the human-readable why (for example, why a snapshot was reused
// rather than re-frozen); Batch and Batches are zero except on import stages.
type MemoryCustodyProgress struct {
	Stage   MemoryCustodyStage
	Source  string
	Detail  string
	Batch   int
	Batches int
	Rows    int
}

// progress reports one observable step, dropping the callback's refusal into
// the same failure path as any other stage so a cancelled context stops the run.
func (options MemoryCustodyOptions) progress(step MemoryCustodyProgress) error {
	if options.Progress == nil {
		return nil
	}
	if err := options.Progress(step); err != nil {
		return fmt.Errorf("memory custody progress observer: %w", err)
	}
	return nil
}

const (
	// MemoryDriftMutated names a source row whose payload changed after a
	// custody batch had already committed its identity.
	MemoryDriftMutated = "mutated"
	// MemoryDriftDeleted names a source row that disappeared from the source
	// database after a custody batch had already committed its identity.
	MemoryDriftDeleted = "deleted"
)

// MemoryDrift is one source change observed between an interrupted run and its
// resume. Drift is reported rather than fatal: the committed membership stays as
// truthful history, and a mutation is carried forward as an additional version.
type MemoryDrift struct {
	SourceDatabase string
	SourceKey      string
	Kind           string
	PriorDigest    string
	Digest         string
}

type MemoryCustodyReport struct {
	State              migrationledger.State
	Memberships        int
	PhysicalRecords    int
	FTSRecords         int
	VerificationDigest string
	Snapshots          []string
	SnapshotPaths      map[string]string
	Drift              []MemoryDrift
}

type memorySource struct {
	name, path string
}

// memoryVersion is one committed identity of a source row. A source key holds
// its base id for the first version and an id#n suffix for later ones, so
// `memory_compatibility` still casts every version back to its legacy id.
type memoryVersion struct {
	version int
	digest  string
}

type pendingMemory struct {
	row       memoryRow
	sourceKey string
}

type memoryRow struct {
	id             int64
	layer          string
	content        string
	metadata       sql.NullString
	origin         string
	provenance     sql.NullString
	sourceAgent    sql.NullString
	sourceModel    sql.NullString
	sourceSurface  sql.NullString
	sourceSession  sql.NullString
	sourceSequence sql.NullInt64
	project        sql.NullString
	status         sql.NullString
	supersedes     sql.NullInt64
	createdAt      sql.NullString
	expiresAt      sql.NullString
	digest         string
}

// MemoryCustodyCutoverEligible reports whether DATA-2 has verified the ops
// compatibility population. It performs no migration and does not open a
// source database.
func MemoryCustodyCutoverEligible(ctx context.Context, opsPath string,
	timeout ...time.Duration) (bool, error) {
	ops, err := bundledplugin.OpenDatabase(opsPath, true, timeout...)
	if err != nil {
		return false, err
	}
	defer ops.Close()
	return migrationledger.MigrationCutoverEligible(ctx, ops, memoryCustodyMigration)
}

// MemoryCustodyWriterFenced reports whether DATA-2 has begun owning memory
// writes. Once this is true, a read-route rollback must keep new stores in ops.
func MemoryCustodyWriterFenced(ctx context.Context, opsPath string) (fenced bool, resultErr error) {
	ops, err := bundledplugin.OpenDatabase(opsPath, true)
	if err != nil {
		return false, err
	}
	defer func() {
		if err := ops.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close ops writer-fence read: %w", err))
		}
	}()
	state, err := migrationledger.InspectMigration(ctx, ops, memoryCustodyMigration)
	if err != nil {
		return false, err
	}
	return state.State != migrationledger.StateAbsent, nil
}

// MigrateMemoryCustody copies memory identities from verified snapshots into
// ops' hidden DATA-2 tables. The currently served memories and FTS tables are
// never selected as a destination, so a completed shadow migration cannot
// change an answer before cutover.
func MigrateMemoryCustody(ctx context.Context, options MemoryCustodyOptions) (MemoryCustodyReport, error) {
	if err := options.valid(); err != nil {
		return MemoryCustodyReport{}, err
	}
	batchSize := options.BatchSize
	if batchSize == 0 {
		batchSize = defaultMemoryBatchSize
	}

	release, err := custodyLock(options.LockPath)
	if err != nil {
		return MemoryCustodyReport{}, err
	}
	defer release()

	ops, err := bundledplugin.OpenDatabase(options.OpsPath, false)
	if err != nil {
		return MemoryCustodyReport{}, fmt.Errorf("open ops memory custody: %w", err)
	}
	defer ops.Close()
	plugin, err := migrationledger.Inspect(ctx, ops)
	if err != nil {
		return MemoryCustodyReport{}, err
	}
	if err := migrationledger.PrepareMigration(ctx, ops, migrationledger.Migration{
		Name: memoryCustodyMigration, DestinationTable: memoryCustodyDestination,
	}); err != nil {
		return MemoryCustodyReport{}, err
	}
	state, err := migrationledger.InspectMigration(ctx, ops, memoryCustodyMigration)
	if err != nil {
		return MemoryCustodyReport{}, err
	}
	sources := []memorySource{
		{name: opsMemorySource, path: options.OpsPath},
		{name: coreMemorySource, path: options.CorePath},
		{name: corpusMemorySource, path: options.CorpusPath},
	}
	if state.State == migrationledger.StateVerified ||
		state.State == migrationledger.StateVerifiedEmpty {
		report, err := inspectMemoryCustody(ctx, ops, state, options.SnapshotDir, plugin)
		if err != nil {
			return MemoryCustodyReport{}, err
		}
		for _, source := range sources {
			path := report.SnapshotPaths[source.name]
			info, statErr := os.Stat(path)
			switch {
			case statErr == nil && info.Mode().IsRegular():
				continue
			case statErr == nil:
				return MemoryCustodyReport{}, fmt.Errorf("%s memory snapshot is not a regular file", source.name)
			case !errors.Is(statErr, os.ErrNotExist):
				return MemoryCustodyReport{}, fmt.Errorf("inspect %s memory snapshot: %w", source.name, statErr)
			}
			return MemoryCustodyReport{}, fmt.Errorf("the verified %s memory snapshot is missing: %w", source.name, statErr)
		}
		return report, nil
	}
	if state.State != migrationledger.StatePrepared && state.State != migrationledger.StateBatchInProgress {
		return MemoryCustodyReport{}, fmt.Errorf(
			"ops memory custody is %q, want prepared or batch-in-progress", state.State)
	}

	snapshots := make([]string, 0, len(sources))
	snapshotPaths := make(map[string]string, len(sources))
	rowsBySource := make(map[string][]memoryRow, len(sources))
	var drift []MemoryDrift
	for _, source := range sources {
		snapshot := memorySnapshotPath(options.SnapshotDir, source.name, plugin)
		snapshots = append(snapshots, snapshot)
		snapshotPaths[source.name] = snapshot
		rows, _, snapshotErr := ensureMemorySnapshot(ctx, source, snapshot, options)
		if snapshotErr != nil {
			return custodyFailure(ctx, ops, snapshots, drift, snapshotErr)
		}
		rowsBySource[source.name] = rows
	}

	for _, source := range sources {
		rows := rowsBySource[source.name]
		history, historyErr := importedMemoryHistory(ctx, ops, source.name)
		if historyErr != nil {
			return custodyFailure(ctx, ops, snapshots, drift, historyErr)
		}
		pending, sourceDrift := pendingMemories(source.name, rows, history)
		drift = append(drift, sourceDrift...)
		batches := (len(pending) + batchSize - 1) / batchSize
		if batches > 0 {
			if progressErr := options.progress(MemoryCustodyProgress{Stage: StageImport,
				Source: source.name,
				Detail: fmt.Sprintf("importing %d pending memories in %d batches", len(pending), batches)}); progressErr != nil {
				return custodyFailure(ctx, ops, snapshots, drift, progressErr)
			}
		}
		ordinal := 0
		for first := 0; first < len(pending); first += batchSize {
			last := min(first+batchSize, len(pending))
			group := pending[first:last]
			batchID := memoryBatchID(source.name, group)
			batch, beginErr := migrationledger.BeginBatch(ctx, ops, migrationledger.BatchSpec{
				Migration: memoryCustodyMigration, ID: batchID,
				SourceDatabase: source.name, SourceTable: "memories",
			})
			if beginErr != nil {
				if errors.Is(beginErr, migrationledger.ErrBatchCommitted) {
					continue
				}
				return custodyFailure(ctx, ops, snapshots, drift, beginErr)
			}
			commit, importErr := importMemoryBatch(ctx, batch, source.name, group)
			if importErr != nil {
				_ = batch.Rollback()
				return custodyFailure(ctx, ops, snapshots, drift, importErr)
			}
			if commitErr := batch.Commit(ctx, commit); commitErr != nil {
				return custodyFailure(ctx, ops, snapshots, drift, commitErr)
			}
			ordinal++
			if options.AfterBatch != nil {
				progress := MemoryBatch{ID: batchID, SourceDatabase: source.name,
					RowCount: len(group), HighWaterMark: commit.HighWaterMark,
					Batch: ordinal, Batches: batches}
				if callbackErr := options.AfterBatch(progress); callbackErr != nil {
					return custodyFailure(ctx, ops, snapshots, drift, callbackErr)
				}
			}
		}
	}

	if err := options.progress(MemoryCustodyProgress{Stage: StageFTSRebuild,
		Detail: "rebuilding the ops memory FTS index"}); err != nil {
		return custodyFailure(ctx, ops, snapshots, drift, err)
	}
	if _, err := ops.ExecContext(ctx,
		"INSERT INTO memory_records_fts(memory_records_fts) VALUES ('rebuild')"); err != nil {
		return custodyFailure(ctx, ops, snapshots, drift, fmt.Errorf("rebuild ops memory FTS: %w", err))
	}
	if err := options.progress(MemoryCustodyProgress{Stage: StageVerify,
		Detail: "verifying ops memory custody"}); err != nil {
		return custodyFailure(ctx, ops, snapshots, drift, err)
	}
	report, digest, err := verifyMemoryCustody(ctx, ops, rowsBySource)
	if err != nil {
		return custodyFailure(ctx, ops, snapshots, drift, err)
	}
	verified, err := recordMemoryVerification(ctx, ops, digest, report.Memberships)
	if err != nil {
		return custodyFailure(ctx, ops, snapshots, drift, err)
	}
	report.State = verified
	report.VerificationDigest = digest
	report.Snapshots = snapshots
	report.SnapshotPaths = snapshotPaths
	report.Drift = drift
	return report, nil
}

// pendingMemories decides what each live source row still owes ops custody.
// A row whose latest committed digest already matches is done; a row that has
// drifted is carried forward as a further version rather than refused, so an
// ordinary ingest between an interruption and its resume cannot wedge the
// migration. Rows that vanished from the source keep their committed
// membership: custody records what a source once held, not only what it holds.
func pendingMemories(source string, rows []memoryRow,
	history map[int64][]memoryVersion) ([]pendingMemory, []MemoryDrift) {
	pending := make([]pendingMemory, 0, len(rows))
	var drift []MemoryDrift
	live := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		live[row.id] = struct{}{}
		versions, found := history[row.id]
		if !found {
			pending = append(pending, pendingMemory{row: row, sourceKey: memorySourceKey(row.id, 1)})
			continue
		}
		latest := versions[len(versions)-1]
		if latest.digest == row.digest {
			continue
		}
		next := latest.version + 1
		pending = append(pending, pendingMemory{row: row, sourceKey: memorySourceKey(row.id, next)})
		drift = append(drift, MemoryDrift{SourceDatabase: source, SourceKey: memorySourceKey(row.id, next),
			Kind: MemoryDriftMutated, PriorDigest: latest.digest, Digest: row.digest})
	}
	for id, versions := range history {
		if _, found := live[id]; found {
			continue
		}
		latest := versions[len(versions)-1]
		drift = append(drift, MemoryDrift{SourceDatabase: source,
			SourceKey: memorySourceKey(id, latest.version), Kind: MemoryDriftDeleted,
			PriorDigest: latest.digest})
	}
	sortMemoryDrift(drift)
	return pending, drift
}

// recordMemoryVerification separates the two verified outcomes: a population
// that carried memories reaches the terminal verified state, while a home whose
// three sources were all empty reaches verified-empty. Both are terminal and
// preserve the frozen inputs needed to resume the remaining custody stages.
//
// The two are told apart by this migration's own membership count rather than by
// any batch the ops ledger holds, so a batch some other rung commits into the
// same plugin database can never make an empty memory population look carried
// and seal it.
func recordMemoryVerification(ctx context.Context, ops *sql.DB, digest string,
	memberships int) (migrationledger.State, error) {
	if memberships == 0 {
		if err := migrationledger.VerifyMigrationEmpty(ctx, ops,
			memoryCustodyMigration, digest); err != nil {
			return "", err
		}
		return migrationledger.StateVerifiedEmpty, nil
	}
	if err := migrationledger.VerifyMigration(ctx, ops, memoryCustodyMigration, digest); err != nil {
		return "", err
	}
	return migrationledger.StateVerified, nil
}

// custodyFailure keeps a failed report honest about the ledger on disk, so a
// driver can tell "nothing was committed" from "some batches committed" without
// a second inspection.
func custodyFailure(ctx context.Context, ops *sql.DB, snapshots []string,
	drift []MemoryDrift, err error) (MemoryCustodyReport, error) {
	report := MemoryCustodyReport{Snapshots: snapshots, Drift: drift}
	if state, inspectErr := migrationledger.InspectMigration(ctx, ops,
		memoryCustodyMigration); inspectErr == nil {
		report.State = state.State
	}
	return report, err
}

func (options MemoryCustodyOptions) valid() error {
	if strings.TrimSpace(options.CorePath) == "" || strings.TrimSpace(options.CorpusPath) == "" ||
		strings.TrimSpace(options.OpsPath) == "" || strings.TrimSpace(options.SnapshotDir) == "" {
		return fmt.Errorf("memory custody needs core, corpus, ops, and snapshot paths")
	}
	if options.BatchSize < 0 {
		return fmt.Errorf("memory custody batch size cannot be negative")
	}
	return nil
}

func custodyLock(path string) (func() error, error) {
	if path == "" {
		return func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create memory custody lock directory: %w", err)
	}
	release, err := securefile.Lock(path)
	if err != nil {
		return nil, fmt.Errorf("lock memory custody sources: %w", err)
	}
	return release, nil
}

// memorySnapshotPath names one source's frozen copy deterministically per
// migration generation, so a retried or resumed run replaces its own previous
// copy instead of leaving another full database behind on every attempt. The
// name is known before the copy exists, which lets the caller register the path
// first and never orphan a partially written snapshot.
func memorySnapshotPath(directory, source string, state migrationledger.Snapshot) string {
	name := strings.NewReplacer(":", "-", "/", "-").Replace(source)
	return filepath.Join(directory,
		fmt.Sprintf(".%s-schema%d-index%d.snapshot.db", name, state.SchemaVersion, state.IndexVersion))
}

// ensureMemorySnapshot publishes the frozen copy the migration reads from
// without re-VACUUMing when an interrupted run already left a faithful one
// (issue #455's resume cost). An existing snapshot is kept only when it passes
// integrity_check and a fresh read of the live source still digests to the same
// identities: a stale copy would silently drop everything ingested since it was
// frozen, so drift re-freezes rather than skipping. The returned rows are the
// snapshot identities either way, and reused reports whether the existing file
// was kept.
func ensureMemorySnapshot(ctx context.Context, source memorySource, snapshot string,
	options MemoryCustodyOptions) ([]memoryRow, bool, error) {
	refreeze := func(why string) ([]memoryRow, bool, error) {
		if err := options.progress(MemoryCustodyProgress{Stage: StageSnapshot,
			Source: source.name, Detail: why}); err != nil {
			return nil, false, err
		}
		if err := snapshotMemories(ctx, source, snapshot); err != nil {
			return nil, false, err
		}
		rows, err := readMemoryRows(ctx, source.name, snapshot)
		if err != nil {
			return nil, false, err
		}
		return rows, false, nil
	}
	if _, statErr := os.Stat(snapshot); statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			return nil, false, fmt.Errorf("inspect the existing %s memory snapshot: %w", source.name, statErr)
		}
		return refreeze(fmt.Sprintf("freezing the first %s memory snapshot", source.name))
	}
	if integrityErr := snapshotIntegrity(ctx, source.name, snapshot); integrityErr != nil {
		return refreeze(fmt.Sprintf("the existing %s memory snapshot failed integrity_check (%s); re-freezing",
			source.name, integrityErr))
	}
	existing, err := readMemoryRows(ctx, source.name, snapshot)
	if err != nil {
		return refreeze(fmt.Sprintf("the existing %s memory snapshot is unreadable; re-freezing", source.name))
	}
	live, err := readMemoryRows(ctx, source.name, source.path)
	if err != nil {
		return nil, false, fmt.Errorf("read the live %s memories to prove its snapshot current: %w", source.name, err)
	}
	if !sameMemoryIdentities(existing, live) {
		return refreeze(fmt.Sprintf("the live %s memories moved since the snapshot was frozen; re-freezing",
			source.name))
	}
	if err := options.progress(MemoryCustodyProgress{Stage: StageSnapshot, Source: source.name,
		Detail: fmt.Sprintf("reusing the existing %s memory snapshot: it passes integrity_check and matches the live source",
			source.name)}); err != nil {
		return nil, false, err
	}
	return existing, true, nil
}

// snapshotIntegrity runs the SQLite integrity check over a published snapshot,
// so a resume never builds on a copy that disk damage touched after it was
// verified at birth.
func snapshotIntegrity(ctx context.Context, source, path string) error {
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("run integrity_check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity_check = %q", integrity)
	}
	return nil
}

// sameMemoryIdentities reports whether two reads of one memory source hold the
// same population: identical id sets whose canonical digests agree. A digest
// covers every carried column, so matching digests mean matching content.
func sameMemoryIdentities(left, right []memoryRow) bool {
	if len(left) != len(right) {
		return false
	}
	digests := make(map[int64]string, len(left))
	for _, row := range left {
		digests[row.id] = row.digest
	}
	for _, row := range right {
		if digest, found := digests[row.id]; !found || digest != row.digest {
			return false
		}
	}
	return true
}

// snapshotMemories freezes one source onto its registered generation path by way
// of a sibling partial copy: the replacement is written and verified first, then
// renamed into place. A failed copy therefore leaves the previously verified
// snapshot intact, and no reader of the registered path can ever observe a
// half-written database.
func snapshotMemories(ctx context.Context, source memorySource, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create memory snapshot directory: %w", err)
	}
	partial := path + ".partial"
	defer os.Remove(partial)
	if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear the previous partial %s memory snapshot: %w", source.name, err)
	}
	if err := freezeMemorySnapshot(ctx, source, partial); err != nil {
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		return fmt.Errorf("publish the %s memory snapshot: %w", source.name, err)
	}
	return nil
}

func freezeMemorySnapshot(ctx context.Context, source memorySource, partial string) error {
	sourceDB, err := bundledplugin.OpenDatabase(source.path, true)
	if err != nil {
		return fmt.Errorf("open %s memory source: %w", source.name, err)
	}
	defer sourceDB.Close()
	if _, err := sourceDB.ExecContext(ctx, "VACUUM INTO ?", partial); err != nil {
		return fmt.Errorf("freeze %s memories: %w", source.name, err)
	}
	if err := os.Chmod(partial, 0o600); err != nil {
		return fmt.Errorf("restrict %s memory snapshot: %w", source.name, err)
	}

	copyDB, err := bundledplugin.OpenDatabase(partial, true)
	if err != nil {
		return fmt.Errorf("open %s memory snapshot: %w", source.name, err)
	}
	defer copyDB.Close()
	var sourceCount, copyCount int
	var integrity string
	if err := sourceDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&sourceCount); err != nil {
		return fmt.Errorf("count %s source memories: %w", source.name, err)
	}
	if err := copyDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&copyCount); err != nil {
		return fmt.Errorf("count %s snapshot memories: %w", source.name, err)
	}
	if err := copyDB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("check %s memory snapshot: %w", source.name, err)
	}
	if sourceCount != copyCount || integrity != "ok" {
		return fmt.Errorf("%s memory snapshot verification failed: source=%d copy=%d integrity=%s",
			source.name, sourceCount, copyCount, integrity)
	}
	return nil
}

func readMemoryRows(ctx context.Context, source, path string) ([]memoryRow, error) {
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	columns, err := memoryColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	provenance := "NULL"
	if columns["provenance"] {
		provenance = "provenance"
	}
	expiresAt := "NULL"
	if columns["expires_at"] {
		expiresAt = "expires_at"
	}
	query := `SELECT id, layer, content, metadata, origin, ` + provenance + `,
		source_agent, source_model, source_surface, source_session, source_sequence,
		project, status, supersedes, created_at, ` + expiresAt + ` FROM memories ORDER BY id`
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read %s memory snapshot: %w", source, err)
	}
	defer result.Close()
	var rows []memoryRow
	for result.Next() {
		var row memoryRow
		if err := result.Scan(&row.id, &row.layer, &row.content, &row.metadata, &row.origin,
			&row.provenance, &row.sourceAgent, &row.sourceModel, &row.sourceSurface,
			&row.sourceSession, &row.sourceSequence, &row.project, &row.status,
			&row.supersedes, &row.createdAt, &row.expiresAt); err != nil {
			return nil, fmt.Errorf("scan %s memory: %w", source, err)
		}
		row.digest = row.canonicalDigest()
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("read %s memories: %w", source, err)
	}
	return rows, nil
}

func memoryColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	return bundledplugin.TableColumns(ctx, db, "memories")
}

func importMemoryBatch(ctx context.Context, batch *migrationledger.Batch,
	source string, rows []pendingMemory) (migrationledger.BatchCommit, error) {
	digest := sha256.New()
	for _, pending := range rows {
		row := pending.row
		destination, found, err := aliasDestination(ctx, batch, source, row.digest)
		if err != nil {
			return migrationledger.BatchCommit{}, err
		}
		if !found {
			result, insertErr := batch.ExecContext(ctx, `INSERT INTO memory_records
				(canonical_digest, provenance, layer, content, metadata, origin, source_agent,
				 source_model, source_surface, source_session, source_sequence, project, status,
				 supersedes, created_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.digest, source,
				row.layer, row.content, nullableString(row.metadata), row.origin,
				nullableString(row.sourceAgent), nullableString(row.sourceModel),
				nullableString(row.sourceSurface), nullableString(row.sourceSession),
				nullableInt(row.sourceSequence), nullableString(row.project), nullableString(row.status),
				nullableInt(row.supersedes), nullableString(row.createdAt), nullableString(row.expiresAt))
			if insertErr != nil {
				return migrationledger.BatchCommit{}, fmt.Errorf("store %s memory %d in ops custody: %w",
					source, row.id, insertErr)
			}
			destination, err = result.LastInsertId()
			if err != nil {
				return migrationledger.BatchCommit{}, fmt.Errorf("read ops memory destination: %w", err)
			}
		}
		sourceKey := pending.sourceKey
		if _, err := batch.ExecContext(ctx, `INSERT INTO memory_provenance
			(source_database, source_key, provenance) VALUES (?, ?, ?)`, source, sourceKey,
			nullableString(row.provenance)); err != nil {
			return migrationledger.BatchCommit{}, fmt.Errorf("store %s memory provenance: %w", source, err)
		}
		if err := batch.AddMembership(ctx, migrationledger.Membership{
			SourceKey: sourceKey, DestinationTable: "memory_records",
			DestinationKey: strconv.FormatInt(destination, 10), CanonicalDigest: row.digest,
		}); err != nil {
			return migrationledger.BatchCommit{}, err
		}
		writeField(digest, source)
		writeField(digest, sourceKey)
		writeField(digest, strconv.FormatInt(destination, 10))
		writeField(digest, row.digest)
	}
	return migrationledger.BatchCommit{
		RowCount: len(rows), CanonicalDigest: fmt.Sprintf("%x", digest.Sum(nil)),
		HighWaterMark: strconv.FormatInt(rows[len(rows)-1].row.id, 10),
	}, nil
}

// aliasDestination performs a multiset union, not ordinary deduplication. One
// physical record can represent at most one identity from each source. A second
// byte-equal core row therefore remains physical even when the first one aliases
// a corpus row, preserving the historical duplicate population.
func aliasDestination(ctx context.Context, batch *migrationledger.Batch,
	source, digest string) (int64, bool, error) {
	// destination_key is written as strconv.FormatInt, so text equality against
	// CAST(records.id AS TEXT) is exactly the integer equality the CAST form
	// expressed while keeping the predicate sargable for the
	// custody_memberships(migration, destination_key) index (issue #455).
	// Use the writer transaction: a separate connection blocks when dirty pages
	// spill in DELETE journal mode. The query also sees this batch's memberships,
	// so its same-source predicate excludes destinations already used in the batch.
	rows, err := batch.QueryContext(ctx, memoryAliasQuery, memoryCustodyMigration,
		digest, source, memoryCustodyMigration, source)
	if err != nil {
		return 0, false, fmt.Errorf("look for an exact cross-source memory: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		var destination int64
		if err := rows.Scan(&destination); err != nil {
			return 0, false, fmt.Errorf("read an exact cross-source memory: %w", err)
		}
		return destination, true, nil
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("look for an exact cross-source memory: %w", err)
	}
	return 0, false, nil
}

// memoryBatchID is derived from the source keys the batch actually carries, so
// re-importing a drifted row as a further version never collides with the batch
// that carried its earlier version, while an identical retry stays recognized as
// already committed.
func memoryBatchID(source string, rows []pendingMemory) string {
	name := strings.NewReplacer(":", "-", "/", "-").Replace(source)
	keys := sha256.New()
	for _, pending := range rows {
		writeField(keys, pending.sourceKey)
	}
	return fmt.Sprintf("data2-%s-memories-%020d-%020d-%x", name, rows[0].row.id,
		rows[len(rows)-1].row.id, keys.Sum(nil)[:8])
}

func memorySourceKey(id int64, version int) string {
	if version <= 1 {
		return strconv.FormatInt(id, 10)
	}
	return strconv.FormatInt(id, 10) + "#" + strconv.Itoa(version)
}

func parseMemorySourceKey(key string) (int64, int, error) {
	base, suffix, versioned := strings.Cut(key, "#")
	id, err := strconv.ParseInt(base, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	if !versioned {
		return id, 1, nil
	}
	version, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, 0, err
	}
	if version < 2 {
		return 0, 0, fmt.Errorf("versioned memory key %q must count from 2", key)
	}
	return id, version, nil
}

// importedMemoryHistory reads every committed identity of every source row,
// newest version last, so a resume can tell an unchanged row from a drifted one
// without discarding what an earlier batch truthfully recorded.
func importedMemoryHistory(ctx context.Context, db *sql.DB, source string) (map[int64][]memoryVersion, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_key, canonical_digest
		FROM custody_memberships
		WHERE migration = ? AND source_database = ? AND source_table = 'memories'`,
		memoryCustodyMigration, source)
	if err != nil {
		return nil, fmt.Errorf("read imported %s memory identities: %w", source, err)
	}
	defer rows.Close()
	imported := make(map[int64][]memoryVersion)
	for rows.Next() {
		var sourceKey, digest string
		if err := rows.Scan(&sourceKey, &digest); err != nil {
			return nil, fmt.Errorf("read imported %s memory identity: %w", source, err)
		}
		id, version, err := parseMemorySourceKey(sourceKey)
		if err != nil {
			return nil, fmt.Errorf("read imported %s memory key %q: %w", source, sourceKey, err)
		}
		imported[id] = append(imported[id], memoryVersion{version: version, digest: digest})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read imported %s memory identities: %w", source, err)
	}
	for id := range imported {
		slices.SortFunc(imported[id], func(left, right memoryVersion) int {
			return left.version - right.version
		})
	}
	return imported, nil
}

func sortMemoryDrift(drift []MemoryDrift) {
	slices.SortFunc(drift, func(left, right MemoryDrift) int {
		if left.Kind != right.Kind {
			return strings.Compare(left.Kind, right.Kind)
		}
		return strings.Compare(left.SourceKey, right.SourceKey)
	})
}

func verifyMemoryCustody(ctx context.Context, ops *sql.DB,
	rowsBySource map[string][]memoryRow) (MemoryCustodyReport, string, error) {
	var integrity string
	if err := ops.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return MemoryCustodyReport{}, "", fmt.Errorf("read ops memory integrity check: %w", err)
	}
	if integrity != "ok" {
		return MemoryCustodyReport{}, "", fmt.Errorf("ops memory integrity check = %q", integrity)
	}
	var foreignKeys, memberships, physical, expectedPhysical, fts, orphans, missingFTS int
	checks := []struct {
		query string
		args  []any
		into  *int
	}{
		{query: "SELECT COUNT(*) FROM pragma_foreign_key_check", into: &foreignKeys},
		{query: "SELECT COUNT(*) FROM custody_memberships WHERE migration = ?",
			args: []any{memoryCustodyMigration}, into: &memberships},
		{query: "SELECT COUNT(*) FROM memory_records", into: &physical},
		{query: `SELECT COALESCE(SUM(maximum), 0) FROM (
			SELECT canonical_digest, MAX(source_count) AS maximum FROM (
				SELECT canonical_digest, source_database, COUNT(*) AS source_count
				FROM custody_memberships WHERE migration = ?
				GROUP BY canonical_digest, source_database)
			GROUP BY canonical_digest)`, args: []any{memoryCustodyMigration}, into: &expectedPhysical},
		{query: "SELECT COUNT(*) FROM memory_records_fts_docsize", into: &fts},
		{query: memoryOrphansQuery,
			args: []any{memoryCustodyMigration}, into: &orphans},
		{query: `SELECT COUNT(*) FROM memory_records AS records
			LEFT JOIN memory_records_fts_docsize AS indexed ON indexed.id = records.id
			WHERE indexed.id IS NULL`, into: &missingFTS},
	}
	for _, check := range checks {
		if err := ops.QueryRowContext(ctx, check.query, check.args...).Scan(check.into); err != nil {
			return MemoryCustodyReport{}, "", fmt.Errorf("verify ops memory custody: %w", err)
		}
	}
	expectedMemberships := 0
	for source, rows := range rowsBySource {
		history, err := importedMemoryHistory(ctx, ops, source)
		if err != nil {
			return MemoryCustodyReport{}, "", err
		}
		for _, row := range rows {
			versions, found := history[row.id]
			if !found || versions[len(versions)-1].digest != row.digest {
				return MemoryCustodyReport{}, "",
					fmt.Errorf("%s memory %d is not held in its current version by ops custody", source, row.id)
			}
		}
		var recorded, got int
		if err := ops.QueryRowContext(ctx, `SELECT COALESCE(SUM(row_count), 0) FROM migration_batches
			WHERE migration = ? AND source_database = ? AND source_table = 'memories'`,
			memoryCustodyMigration, source).Scan(&recorded); err != nil {
			return MemoryCustodyReport{}, "", err
		}
		if err := ops.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
			WHERE migration = ? AND source_database = ?`,
			memoryCustodyMigration, source).Scan(&got); err != nil {
			return MemoryCustodyReport{}, "", err
		}
		if got != recorded {
			return MemoryCustodyReport{}, "",
				fmt.Errorf("%s memory memberships = %d, want the %d its batches recorded", source, got, recorded)
		}
		expectedMemberships += recorded
	}
	if foreignKeys != 0 || memberships != expectedMemberships || physical != expectedPhysical ||
		fts != physical || orphans != 0 || missingFTS != 0 {
		return MemoryCustodyReport{}, "", fmt.Errorf(
			"ops memory verification failed: foreign_keys=%d memberships=%d/%d physical=%d/%d fts=%d orphans=%d missing_fts=%d",
			foreignKeys, memberships, expectedMemberships, physical, expectedPhysical, fts, orphans, missingFTS)
	}
	if _, err := ops.ExecContext(ctx,
		"INSERT INTO memory_records_fts(memory_records_fts, rank) VALUES ('integrity-check', 1)"); err != nil {
		return MemoryCustodyReport{}, "", fmt.Errorf("verify ops memory FTS content: %w", err)
	}

	digest, err := memoryVerificationDigest(ctx, ops, memberships, physical, fts)
	if err != nil {
		return MemoryCustodyReport{}, "", err
	}
	return MemoryCustodyReport{Memberships: memberships, PhysicalRecords: physical, FTSRecords: fts}, digest, nil
}

func inspectMemoryCustody(ctx context.Context, ops *sql.DB, state migrationledger.MigrationSnapshot,
	snapshotDir string, plugin migrationledger.Snapshot) (MemoryCustodyReport, error) {
	report := MemoryCustodyReport{State: state.State, VerificationDigest: state.VerificationDigest}
	for _, source := range []string{opsMemorySource, coreMemorySource, corpusMemorySource} {
		path := memorySnapshotPath(snapshotDir, source, plugin)
		report.Snapshots = append(report.Snapshots, path)
		if report.SnapshotPaths == nil {
			report.SnapshotPaths = make(map[string]string, 3)
		}
		report.SnapshotPaths[source] = path
	}
	queries := []struct {
		statement string
		args      []any
		target    *int
	}{
		{statement: "SELECT COUNT(*) FROM custody_memberships WHERE migration = ?",
			args: []any{memoryCustodyMigration}, target: &report.Memberships},
		{statement: "SELECT COUNT(*) FROM memory_records", target: &report.PhysicalRecords},
		{statement: "SELECT COUNT(*) FROM memory_records_fts_docsize", target: &report.FTSRecords},
	}
	for _, query := range queries {
		if err := ops.QueryRowContext(ctx, query.statement, query.args...).Scan(query.target); err != nil {
			return MemoryCustodyReport{}, err
		}
	}
	return report, nil
}

func memoryVerificationDigest(ctx context.Context, ops *sql.DB,
	memberships, physical, fts int) (string, error) {
	digest := sha256.New()
	for _, value := range []int{memberships, physical, fts} {
		writeField(digest, strconv.Itoa(value))
	}
	rows, err := ops.QueryContext(ctx, `SELECT batch_id, source_database, source_table,
		row_count, canonical_digest, high_water_mark FROM migration_batches
		WHERE migration = ? ORDER BY batch_id`, memoryCustodyMigration)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, source, table, canonical, highWater string
		var count int
		if err := rows.Scan(&id, &source, &table, &count, &canonical, &highWater); err != nil {
			return "", err
		}
		for _, field := range []string{id, source, table, strconv.Itoa(count), canonical, highWater} {
			writeField(digest, field)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func (row memoryRow) canonicalDigest() string {
	digest := sha256.New()
	writeField(digest, row.layer)
	writeField(digest, row.content)
	writeNullString(digest, row.metadata)
	writeField(digest, row.origin)
	writeNullString(digest, row.sourceAgent)
	writeNullString(digest, row.sourceModel)
	writeNullString(digest, row.sourceSurface)
	writeNullString(digest, row.sourceSession)
	writeNullInt(digest, row.sourceSequence)
	writeNullString(digest, row.project)
	writeNullString(digest, row.status)
	writeNullInt(digest, row.supersedes)
	writeNullString(digest, row.createdAt)
	writeNullString(digest, row.expiresAt)
	return fmt.Sprintf("%x", digest.Sum(nil))
}

func writeField(writer io.Writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}

func writeNullString(writer io.Writer, value sql.NullString) {
	if !value.Valid {
		writeField(writer, "\x00")
		return
	}
	writeField(writer, "\x01"+value.String)
}

func writeNullInt(writer io.Writer, value sql.NullInt64) {
	if !value.Valid {
		writeField(writer, "\x00")
		return
	}
	writeField(writer, "\x01"+strconv.FormatInt(value.Int64, 10))
}

func nullableString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
