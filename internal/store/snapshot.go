/**
 * @overview Creates leased read-only SQLite snapshots. ~700 lines, 6 public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. Start at OpenReadOnlySnapshot  <- coordinates reuse and creation
 *   2. createReadOnlySnapshot         <- publishes a leased copy
 *   3. scavengeReadOnlySnapshots      <- removes abandoned copies
 *   4. ReadOnlySnapshot.Close         <- releases the final reference
 *
 *   MAIN FLOW
 *   ---------
 *   OpenReadOnlySnapshot -> create/read cache -> hold lease -> Close -> remove directory
 *
 *   PUBLIC API
 *   ----------
 *   ReadOnlySnapshot         Leased read-only database copy.
 *   SetSnapshotLogDir        Selects the snapshot telemetry directory.
 *   OpenReadOnlySnapshot     Opens or reuses a stable source snapshot.
 *   SQL                      Returns the copied database handle.
 *   URI                      Returns the copied database URI.
 *   Close                    Releases one reference and removes the final copy.
 *
 *   INTERNALS
 *   ---------
 *   snapshotArtifact, snapshotLease, snapshotInflight, createReadOnlySnapshot
 *   scavengeReadOnlySnapshots, claimSnapshotDirectory
 *   inspectSnapshotSource, copySnapshotSource, openCopiedSnapshot, cleanupHeldSnapshots
 *
 * @exports ReadOnlySnapshot, SetSnapshotLogDir, OpenReadOnlySnapshot, SQL, URI, Close
 * @deps database/sql and modernc SQLite; internal/securefile; os/signal and filesystem
 */
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var snapshotArtifacts = [...]string{"", "-wal", "-journal"}

const (
	snapshotDirectoryPrefix    = "roca-read-only-snapshot-"
	snapshotStagingPrefix      = ".roca-snapshot-staging-"
	snapshotReapPrefix         = ".roca-snapshot-reap-"
	snapshotNamespaceLeaseName = ".roca-read-only-snapshot-namespace.lease"
	snapshotLeaseName          = "lease"
	snapshotCopyBufferSize     = 128 * 1024
	snapshotLogStream          = "snapshots"
	snapshotSignalCleanupLimit = 500 * time.Millisecond
	bytesPerMB                 = 1024 * 1024
)

// -- 1/7 HELPER · Snapshot state and coordination --

type ReadOnlySnapshot struct {
	artifact  *snapshotArtifact
	directory string
	once      sync.Once
	err       error
}

type snapshotArtifact struct {
	database    *sql.DB
	uri         string
	directory   string
	lease       *snapshotLease
	fingerprint string
	refs        int
	once        sync.Once
	err         error
}

type snapshotArtifactState struct {
	exists  bool
	size    int64
	modTime int64
}

type snapshotSourceState [len(snapshotArtifacts)]snapshotArtifactState

type snapshotInflight struct {
	ready chan struct{}
	err   error
}

type snapshotLease struct {
	mu        sync.Mutex
	directory string
	release   func() error
	once      sync.Once
	err       error
}

var (
	snapshotLogDir       atomic.Pointer[string]
	snapshotShuttingDown atomic.Bool

	snapshotCacheMu sync.Mutex
	snapshotCache   = map[string]*snapshotArtifact{}
	snapshotFlight  = map[string]*snapshotInflight{}

	snapshotNamespaceMu sync.Mutex

	snapshotHeldMu sync.Mutex
	snapshotHeld   = map[*snapshotLease]struct{}{}

	snapshotExitOnce sync.Once
)

var (
	copySnapshotSourceFn              = copySnapshotSource
	snapshotBeforeLeaseRegistrationFn func(string)
	claimSnapshotDirectoryFn          = claimSnapshotDirectory
	removeSnapshotDirectoryFn         = os.RemoveAll
)

// SetSnapshotLogDir routes snapshot create/reap records to the standard JSONL
// telemetry logs under dataDir/logs. Empty disables them.
func SetSnapshotLogDir(dataDir string) {
	if strings.TrimSpace(dataDir) == "" {
		snapshotLogDir.Store(nil)
		return
	}
	dir := dataDir
	snapshotLogDir.Store(&dir)
}

// -/ 1/7

// -- 2/7 CORE · OpenReadOnlySnapshot cache and flights -- <- START HERE

func OpenReadOnlySnapshot(ctx context.Context, path string) (*ReadOnlySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshotShuttingDown.Load() {
		return nil, errSnapshotShuttingDown
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	ensureSnapshotExitCleanup()
	if err := scavengeReadOnlySnapshots(ctx, os.TempDir()); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if snapshotShuttingDown.Load() {
			return nil, errSnapshotShuttingDown
		}
		before, err := inspectSnapshotSource(abs)
		if err != nil {
			return nil, err
		}
		fingerprint := snapshotFingerprint(abs, before)

		snapshotCacheMu.Lock()
		if snapshotShuttingDown.Load() {
			snapshotCacheMu.Unlock()
			return nil, errSnapshotShuttingDown
		}
		if cached, ok := snapshotCache[fingerprint]; ok {
			cached.refs++
			snapshotCacheMu.Unlock()
			return newReadOnlySnapshot(cached), nil
		}
		if flight, ok := snapshotFlight[abs]; ok {
			snapshotCacheMu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-flight.ready:
			}
			if flight.err != nil {
				if ctx.Err() == nil && isSnapshotContextError(flight.err) {
					continue
				}
				return nil, flight.err
			}
			continue
		}
		flight := &snapshotInflight{ready: make(chan struct{})}
		snapshotFlight[abs] = flight
		snapshotCacheMu.Unlock()

		created, createErr := createReadOnlySnapshot(ctx, abs, before)
		if createErr == nil && snapshotShuttingDown.Load() {
			createErr = errors.Join(errSnapshotShuttingDown, created.destroy())
			created = nil
		}
		var artifact *snapshotArtifact
		var duplicate *snapshotArtifact
		var rejected *snapshotArtifact
		snapshotCacheMu.Lock()
		if createErr == nil && snapshotShuttingDown.Load() {
			createErr = errSnapshotShuttingDown
			rejected = created
		} else if createErr == nil {
			if cached, ok := snapshotCache[created.fingerprint]; ok {
				cached.refs++
				artifact = cached
				duplicate = created
			} else {
				created.refs = 1
				snapshotCache[created.fingerprint] = created
				artifact = created
			}
		}
		snapshotCacheMu.Unlock()

		if rejected != nil {
			createErr = errors.Join(createErr, rejected.destroy())
		}
		if duplicate != nil {
			if err := duplicate.destroy(); err != nil {
				_ = releaseSnapshotArtifact(artifact)
				artifact = nil
				createErr = err
			}
		}

		snapshotCacheMu.Lock()
		if snapshotFlight[abs] == flight {
			delete(snapshotFlight, abs)
		}
		flight.err = createErr
		close(flight.ready)
		snapshotCacheMu.Unlock()
		if createErr != nil {
			return nil, createErr
		}
		return newReadOnlySnapshot(artifact), nil
	}
}

var errSnapshotShuttingDown = errors.New("process is terminating")

func isSnapshotContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// -/ 2/7

// -- 3/7 HELPER · Snapshot creation and atomic publication --

func createReadOnlySnapshot(ctx context.Context, abs string, before snapshotSourceState) (*snapshotArtifact, error) {
	lease, directory, err := createSnapshotDirectory(os.TempDir())
	if err != nil {
		return nil, err
	}
	abandon := func(cause error) error {
		return lease.destroy(cause)
	}

	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return nil, abandon(err)
		}
		current, err := inspectSnapshotSource(abs)
		if err != nil {
			return nil, abandon(err)
		}
		if current != before {
			before = current
		}
		attemptDirectory := filepath.Join(directory, fmt.Sprintf("%d", attempt))
		if err := os.Mkdir(attemptDirectory, 0o700); err != nil {
			return nil, abandon(err)
		}
		destination := filepath.Join(attemptDirectory, filepath.Base(abs))
		if err := copySnapshotSourceFn(ctx, abs, destination); err != nil {
			after, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != after {
				if cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil); cleanupErr != nil {
					return nil, abandon(cleanupErr)
				}
				before = after
				continue
			}
			return nil, abandon(err)
		}
		after, err := inspectSnapshotSource(abs)
		if err != nil {
			return nil, abandon(err)
		}
		if before != after {
			if err := cleanupSnapshotDirectory(attemptDirectory, nil); err != nil {
				return nil, abandon(err)
			}
			before = after
			continue
		}
		snapshot, err := openCopiedSnapshot(ctx, destination, directory)
		if err != nil {
			final, inspectErr := inspectSnapshotSource(abs)
			if inspectErr == nil && before != final {
				if cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil); cleanupErr != nil {
					return nil, abandon(cleanupErr)
				}
				before = final
				continue
			}
			return nil, abandon(err)
		}
		final, err := inspectSnapshotSource(abs)
		if err == nil && before == final {
			snapshot.lease = lease
			snapshot.fingerprint = snapshotFingerprint(abs, final)
			logSnapshotRecord(map[string]any{
				"event":      "create",
				"source":     abs,
				"size_bytes": snapshotSourceSize(before),
				"reason":     "copy",
			})
			return snapshot, nil
		}
		closeErr := snapshot.database.Close()
		cleanupErr := cleanupSnapshotDirectory(attemptDirectory, nil)
		if closeErr != nil || cleanupErr != nil {
			return nil, abandon(errors.Join(closeErr, cleanupErr))
		}
		if err != nil {
			return nil, abandon(err)
		}
		before = final
	}
	return nil, abandon(fmt.Errorf("database %q kept changing while it was snapshotted", abs))
}

func createSnapshotDirectory(root string) (*snapshotLease, string, error) {
	releaseNamespace, err := lockSnapshotNamespace(root)
	if err != nil {
		return nil, "", fmt.Errorf("lock read-only snapshot namespace: %w", err)
	}
	defer releaseNamespace()

	staging, err := os.MkdirTemp(root, snapshotStagingPrefix)
	if err != nil {
		return nil, "", fmt.Errorf("create read-only snapshot staging directory: %w", err)
	}
	release, err := securefile.Lock(filepath.Join(staging, snapshotLeaseName))
	if err != nil {
		return nil, "", cleanupSnapshotDirectory(staging,
			fmt.Errorf("lock read-only snapshot: %w", err))
	}
	lease := &snapshotLease{directory: staging, release: release}
	if snapshotBeforeLeaseRegistrationFn != nil {
		snapshotBeforeLeaseRegistrationFn(staging)
	}
	if !registerHeldSnapshot(lease) {
		return nil, "", lease.destroy(errSnapshotShuttingDown)
	}
	if err := os.WriteFile(filepath.Join(staging, snapshotLeaseName),
		[]byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return nil, "", lease.destroy(err)
	}

	placeholder, err := os.CreateTemp(root, snapshotDirectoryPrefix)
	if err != nil {
		return nil, "", lease.destroy(fmt.Errorf("reserve read-only snapshot name: %w", err))
	}
	directory := placeholder.Name()
	if err := errors.Join(placeholder.Close(), os.Remove(directory)); err != nil {
		return nil, "", lease.destroy(fmt.Errorf("reserve read-only snapshot name: %w", err))
	}
	if err := lease.move(directory); err != nil {
		return nil, "", lease.destroy(fmt.Errorf("publish read-only snapshot: %w", err))
	}
	return lease, directory, nil
}

func lockSnapshotNamespace(root string) (func() error, error) {
	snapshotNamespaceMu.Lock()
	release, err := securefile.Lock(filepath.Join(root, snapshotNamespaceLeaseName))
	if err != nil {
		snapshotNamespaceMu.Unlock()
		return nil, err
	}
	return func() error {
		err := release()
		snapshotNamespaceMu.Unlock()
		return err
	}, nil
}

func (lease *snapshotLease) move(directory string) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := os.Rename(lease.directory, directory); err != nil {
		return err
	}
	lease.directory = directory
	return nil
}

func (lease *snapshotLease) destroy(cause error) error {
	lease.once.Do(func() {
		unregisterHeldSnapshot(lease)
		lease.mu.Lock()
		defer lease.mu.Unlock()
		var releaseErr error
		if lease.release != nil {
			releaseErr = lease.release()
		}
		lease.err = errors.Join(releaseErr, cleanupSnapshotDirectory(lease.directory, nil))
	})
	return errors.Join(cause, lease.err)
}

// -/ 3/7

// -- 4/7 HELPER · Orphan reaping --

func scavengeReadOnlySnapshots(ctx context.Context, root string) error {
	releaseNamespace, err := lockSnapshotNamespace(root)
	if err != nil {
		return fmt.Errorf("lock read-only snapshot namespace: %w", err)
	}
	defer releaseNamespace()

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect read-only snapshot directory: %w", err)
	}
	var reaped int
	var reclaimed int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() || (!strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) &&
			!strings.HasPrefix(entry.Name(), snapshotStagingPrefix) &&
			!strings.HasPrefix(entry.Name(), snapshotReapPrefix)) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		release, orphan, err := snapshotOrphanLease(path)
		if err != nil || !orphan {
			continue
		}
		claimed, err := claimSnapshotDirectoryFn(root, path)
		if err != nil {
			if release != nil {
				_ = release()
			}
			continue
		}
		size := directorySize(claimed)
		if release != nil {
			_ = release()
		}
		if err := removeSnapshotDirectoryFn(claimed); err != nil {
			continue
		}
		if _, err := os.Stat(claimed); !os.IsNotExist(err) {
			continue
		}
		reaped++
		reclaimed += size
	}
	if reaped > 0 {
		logSnapshotRecord(map[string]any{
			"event":           "reap",
			"count":           reaped,
			"reclaimed_mb":    reclaimed / bytesPerMB,
			"reclaimed_bytes": reclaimed,
		})
	}
	return nil
}

func claimSnapshotDirectory(root, directory string) (string, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("snapshot claim %q is not a directory", directory)
	}
	placeholder, err := os.CreateTemp(root, snapshotReapPrefix)
	if err != nil {
		return "", err
	}
	claimed := placeholder.Name()
	if err := errors.Join(placeholder.Close(), os.Remove(claimed)); err != nil {
		return "", err
	}
	if err := os.Rename(directory, claimed); err != nil {
		return "", err
	}
	return claimed, nil
}

func snapshotOrphanLease(directory string) (func() error, bool, error) {
	release, err := securefile.TryLock(filepath.Join(directory, snapshotLeaseName))
	if err == nil {
		return release, true, nil
	}
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if errors.Is(err, securefile.ErrBusy) {
		return nil, false, nil
	}
	return nil, false, err
}

func cleanupSnapshotDirectory(directory string, cause error) error {
	if err := os.RemoveAll(directory); err != nil {
		return errors.Join(cause, fmt.Errorf("remove read-only snapshot %q: %w", directory, err))
	}
	return cause
}

// -/ 4/7

// -- 5/7 HELPER · Source fingerprinting, copying, and SQLite opening --

func inspectSnapshotSource(path string) (snapshotSourceState, error) {
	var state snapshotSourceState
	for index, suffix := range snapshotArtifacts {
		info, err := os.Stat(path + suffix)
		if err == nil {
			if !info.Mode().IsRegular() {
				return snapshotSourceState{}, fmt.Errorf("snapshot source %q is not a regular file", path+suffix)
			}
			state[index] = snapshotArtifactState{
				exists: true, size: info.Size(), modTime: info.ModTime().UnixNano(),
			}
			continue
		}
		if !os.IsNotExist(err) || suffix == "" {
			return snapshotSourceState{}, fmt.Errorf("inspect snapshot source %q: %w", path+suffix, err)
		}
	}
	return state, nil
}

func snapshotFingerprint(path string, state snapshotSourceState) string {
	var builder strings.Builder
	builder.WriteString(path)
	for _, artifact := range state {
		fmt.Fprintf(&builder, "|%t|%d|%d", artifact.exists, artifact.size, artifact.modTime)
	}
	return builder.String()
}

func snapshotSourceSize(state snapshotSourceState) int64 {
	var total int64
	for _, artifact := range state {
		if artifact.exists {
			total += artifact.size
		}
	}
	return total
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

func copySnapshotSource(ctx context.Context, source, destination string) error {
	for _, suffix := range snapshotArtifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		input, err := os.Open(source + suffix)
		if os.IsNotExist(err) && suffix != "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("open snapshot source %q: %w", source+suffix, err)
		}
		output, err := os.OpenFile(destination+suffix, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create snapshot copy %q: %w", destination+suffix,
				errors.Join(err, input.Close()))
		}
		_, copyErr := io.CopyBuffer(output, &contextReader{ctx: ctx, reader: input},
			make([]byte, snapshotCopyBufferSize))
		err = errors.Join(copyErr, ctx.Err(), input.Close(), output.Close())
		if err != nil {
			return fmt.Errorf("copy snapshot source %q: %w", source+suffix, err)
		}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func openCopiedSnapshot(ctx context.Context, path, directory string) (*snapshotArtifact, error) {
	_, writableURI, err := sqliteFileDSN(path, url.Values{
		"mode": {"rw"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"query_only(1)",
		},
	})
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", writableURI)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	var schemaVersion int
	if err := database.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&schemaVersion); err != nil {
		database.Close()
		return nil, err
	}
	_, uri, err := sqliteFileDSN(path, url.Values{
		"mode": {"ro"},
		"_pragma": {
			fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()),
			"query_only(1)",
		},
	})
	if err != nil {
		database.Close()
		return nil, err
	}
	return &snapshotArtifact{database: database, uri: uri, directory: directory}, nil
}

// -/ 5/7

// -- 6/7 HELPER · Handle lifetime and process cleanup --

func (snapshot *ReadOnlySnapshot) SQL() *sql.DB {
	return snapshot.artifact.database
}

func (snapshot *ReadOnlySnapshot) URI() string {
	return snapshot.artifact.uri
}

func (snapshot *ReadOnlySnapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshot.once.Do(func() {
		snapshot.err = releaseSnapshotArtifact(snapshot.artifact)
	})
	return snapshot.err
}

func newReadOnlySnapshot(artifact *snapshotArtifact) *ReadOnlySnapshot {
	return &ReadOnlySnapshot{artifact: artifact, directory: artifact.directory}
}

func releaseSnapshotArtifact(artifact *snapshotArtifact) error {
	if artifact == nil {
		return nil
	}
	snapshotCacheMu.Lock()
	if artifact.refs > 0 {
		artifact.refs--
	}
	destroy := artifact.refs == 0
	if destroy && snapshotCache[artifact.fingerprint] == artifact {
		delete(snapshotCache, artifact.fingerprint)
	}
	snapshotCacheMu.Unlock()
	if !destroy {
		return nil
	}
	return artifact.destroy()
}

func (artifact *snapshotArtifact) destroy() error {
	artifact.once.Do(func() {
		var closeErr error
		if artifact.database != nil {
			closeErr = artifact.database.Close()
		}
		if artifact.lease != nil {
			artifact.err = artifact.lease.destroy(closeErr)
		} else {
			artifact.err = cleanupSnapshotDirectory(artifact.directory, closeErr)
		}
	})
	return artifact.err
}

func registerHeldSnapshot(lease *snapshotLease) bool {
	snapshotHeldMu.Lock()
	defer snapshotHeldMu.Unlock()
	if snapshotShuttingDown.Load() {
		return false
	}
	snapshotHeld[lease] = struct{}{}
	return true
}

func unregisterHeldSnapshot(lease *snapshotLease) {
	snapshotHeldMu.Lock()
	delete(snapshotHeld, lease)
	snapshotHeldMu.Unlock()
}

func ensureSnapshotExitCleanup() {
	snapshotExitOnce.Do(func() {
		signals := make(chan os.Signal, 1)
		terminating := snapshotTerminationSignals()
		signal.Notify(signals, terminating...)
		go func() {
			sig := <-signals
			signal.Stop(signals)
			beginSnapshotShutdown()
			signal.Reset(terminating...)
			cleaned := make(chan struct{})
			go func() {
				if release, err := lockSnapshotNamespace(os.TempDir()); err == nil {
					_ = release()
				}
				cleanupHeldSnapshots()
				close(cleaned)
			}()
			timer := time.NewTimer(snapshotSignalCleanupLimit)
			select {
			case <-cleaned:
				timer.Stop()
			case <-timer.C:
			}
			if process, err := os.FindProcess(os.Getpid()); err == nil {
				_ = process.Signal(sig)
			}
		}()
	})
}

func beginSnapshotShutdown() {
	snapshotHeldMu.Lock()
	snapshotShuttingDown.Store(true)
	snapshotHeldMu.Unlock()
}

func cleanupHeldSnapshots() {
	snapshotCacheMu.Lock()
	live := make([]*snapshotArtifact, 0, len(snapshotCache))
	for _, artifact := range snapshotCache {
		artifact.refs = 0
		live = append(live, artifact)
	}
	snapshotCache = map[string]*snapshotArtifact{}
	snapshotCacheMu.Unlock()
	for _, artifact := range live {
		_ = artifact.destroy()
	}

	snapshotHeldMu.Lock()
	pending := snapshotHeld
	snapshotHeld = map[*snapshotLease]struct{}{}
	snapshotHeldMu.Unlock()
	for lease := range pending {
		_ = lease.destroy(nil)
	}
}

// -/ 6/7

// -- 7/7 HELPER · JSONL telemetry --

func logSnapshotRecord(record map[string]any) {
	dirp := snapshotLogDir.Load()
	if dirp == nil || *dirp == "" {
		return
	}
	record["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	directory := filepath.Join(*dirp, "logs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return
	}
	path := filepath.Join(directory, snapshotLogStream+"-"+now.Format(time.DateOnly)+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(line, '\n'))
	_ = file.Close()
}

// -/ 7/7
