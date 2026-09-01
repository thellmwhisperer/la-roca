/**
 * @overview Creates leased read-only SQLite snapshots. ~1000 lines, 10 public symbols.
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
 *   SnapshotLogWriter        Snapshot lifecycle telemetry sink.
 *   WithSnapshotLogWriter    Binds snapshot telemetry to an operation context.
 *   WithSnapshotCoordinationTimeout bounds coordination phases.
 *   OpenReadOnlySnapshot     Opens or reuses a stable source snapshot.
 *   SnapshotDirectories      Lists snapshot directories the reaper owns.
 *   CloseReadOnlySnapshots   Closes every snapshot still held by the process.
 *   SQL                      Returns the copied database handle.
 *   URI                      Returns the copied database URI.
 *   Close                    Releases one reference and removes the final copy.
 *
 *   INTERNALS
 *   ---------
 *   snapshotArtifact, snapshotLease, snapshotInflight, snapshotNamespaceRoot
 *   createReadOnlySnapshot, scavengeReadOnlySnapshots
 *   claimSnapshotDirectory
 *   inspectSnapshotSource, copySnapshotSource, openCopiedSnapshot, cleanupHeldSnapshots
 *
 * @exports ReadOnlySnapshot, SnapshotLogWriter, WithSnapshotLogWriter, WithSnapshotCoordinationTimeout, OpenReadOnlySnapshot, SnapshotDirectories, CloseReadOnlySnapshots, SQL, URI, Close
 * @deps database/sql and modernc SQLite; internal/securefile; os/signal and filesystem
 */
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	snapshotNamespacePrefix    = ".roca-snapshot-namespace-"
	snapshotNamespaceLeaseName = ".roca-read-only-snapshot-namespace.lease"
	snapshotLeaseName          = "lease"
	snapshotCopyBufferSize     = 128 * 1024
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
	exists   bool
	size     int64
	modTime  int64
	identity string
}

type snapshotSourceState [len(snapshotArtifacts)]snapshotArtifactState

type snapshotInflight struct {
	ready chan struct{}
	err   error
}

type snapshotCopyInflight struct {
	done chan struct{}
}

type snapshotLease struct {
	mu        sync.Mutex
	directory string
	release   func() error
	once      sync.Once
	err       error
}

// SnapshotLogWriter records snapshot lifecycle telemetry.
type SnapshotLogWriter func(context.Context, map[string]any) error

type snapshotLogContextKey struct{}
type snapshotCoordinationTimeoutContextKey struct{}

var (
	snapshotShuttingDown atomic.Bool

	snapshotShutdownCtx, snapshotShutdownCancel = context.WithCancel(context.Background())

	snapshotCacheMu sync.Mutex
	snapshotCache   = map[string]*snapshotArtifact{}
	snapshotFlight  = map[string]*snapshotInflight{}

	snapshotNamespaceGate = func() chan struct{} {
		gate := make(chan struct{}, 1)
		gate <- struct{}{}
		return gate
	}()

	snapshotHeldMu sync.Mutex
	snapshotHeld   = map[*snapshotLease]struct{}{}

	snapshotCopyMu      sync.Mutex
	snapshotCopyHandles = map[*os.File]struct{}{}
	snapshotCopies      = map[*snapshotCopyInflight]struct{}{}

	snapshotExitOnce sync.Once
)

var (
	copySnapshotSourceFn             = copySnapshotSource
	snapshotAfterLeaseRegistrationFn func(string)
	snapshotAfterCopyHandleFn        func(string)
	claimSnapshotDirectoryFn         = claimSnapshotDirectory
	removeSnapshotDirectoryFn        = os.RemoveAll
	snapshotEntryInfoFn              = func(entry os.DirEntry) (os.FileInfo, error) { return entry.Info() }
	snapshotUserIdentityFn           = snapshotUserIdentity
)

// WithSnapshotLogWriter binds snapshot lifecycle telemetry to ctx.
func WithSnapshotLogWriter(ctx context.Context, writer SnapshotLogWriter) context.Context {
	return context.WithValue(ctx, snapshotLogContextKey{}, writer)
}

// WithSnapshotCoordinationTimeout bounds snapshot coordination phases.
func WithSnapshotCoordinationTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, snapshotCoordinationTimeoutContextKey{}, timeout)
}

// SnapshotDirectories lists every directory under root that carries the
// read-only snapshot prefix the reaper owns. The CLI test suite uses it to
// assert a completed command leaves no snapshot directory behind.
func SnapshotDirectories(root string) ([]string, error) {
	var directories []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) {
			directories = append(directories, path)
			return filepath.SkipDir
		}
		return nil
	})
	return directories, err
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
	logger := snapshotLogWriterFromContext(ctx)
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

		var created *snapshotArtifact
		tempRoot := os.TempDir()
		namespace, createErr := snapshotNamespaceRoot(tempRoot)
		if createErr == nil {
			coordinationCtx, cancel := snapshotCoordinationContext(ctx)
			createErr = scavengeReadOnlySnapshots(coordinationCtx, namespace)
			cancel()
		}
		if createErr == nil {
			created, createErr = createReadOnlySnapshot(ctx, abs, before, namespace, logger)
		}
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
				created.refs = 2
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

func createReadOnlySnapshot(ctx context.Context, abs string, before snapshotSourceState, namespace string,
	logger SnapshotLogWriter,
) (*snapshotArtifact, error) {
	coordinationCtx, cancelCoordination := snapshotCoordinationContext(ctx)
	lease, directory, err := createSnapshotDirectory(coordinationCtx, namespace)
	cancelCoordination()
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
		openCtx, cancelOpen := snapshotCoordinationContext(ctx)
		snapshot, err := openCopiedSnapshot(openCtx, destination, directory)
		cancelOpen()
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
			logCtx, cancelLog := snapshotCoordinationContext(ctx)
			logSnapshotRecord(logCtx, logger, map[string]any{
				"event":      "create",
				"source":     abs,
				"size_bytes": snapshotSourceSize(before),
				"reason":     "copy",
			})
			cancelLog()
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

func createSnapshotDirectory(ctx context.Context, root string) (*snapshotLease, string, error) {
	releaseNamespace, err := lockSnapshotNamespace(ctx, root)
	if err != nil {
		return nil, "", fmt.Errorf("lock read-only snapshot namespace: %w", err)
	}
	defer releaseNamespace()

	snapshotHeldMu.Lock()
	if snapshotShuttingDown.Load() {
		snapshotHeldMu.Unlock()
		return nil, "", errSnapshotShuttingDown
	}
	staging, err := os.MkdirTemp(root, snapshotStagingPrefix)
	if err != nil {
		snapshotHeldMu.Unlock()
		return nil, "", fmt.Errorf("create read-only snapshot staging directory: %w", err)
	}
	leasePath := filepath.Join(staging, snapshotLeaseName)
	if err := os.WriteFile(leasePath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		snapshotHeldMu.Unlock()
		return nil, "", cleanupSnapshotDirectory(staging, err)
	}
	release, err := securefile.LockExisting(leasePath)
	if err != nil {
		snapshotHeldMu.Unlock()
		return nil, "", cleanupSnapshotDirectory(staging,
			fmt.Errorf("lock read-only snapshot: %w", err))
	}
	lease := &snapshotLease{directory: staging, release: release}
	snapshotHeld[lease] = struct{}{}
	snapshotHeldMu.Unlock()
	if snapshotAfterLeaseRegistrationFn != nil {
		snapshotAfterLeaseRegistrationFn(staging)
	}
	if snapshotShuttingDown.Load() {
		return nil, "", lease.destroy(errSnapshotShuttingDown)
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

func lockSnapshotNamespace(ctx context.Context, root string) (func() error, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-snapshotNamespaceGate:
	}
	releaseGate := func() { snapshotNamespaceGate <- struct{}{} }
	leasePath := filepath.Join(root, snapshotNamespaceLeaseName)
	file, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		releaseGate()
		return nil, err
	}
	if err := errors.Join(file.Chmod(0o600), file.Close()); err != nil {
		releaseGate()
		return nil, err
	}
	for {
		release, err := securefile.TryLock(leasePath)
		if err == nil {
			return func() error {
				err := release()
				releaseGate()
				return err
			}, nil
		}
		if !errors.Is(err, securefile.ErrBusy) {
			releaseGate()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			releaseGate()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func snapshotNamespaceRoot(tempRoot string) (string, error) {
	abs, err := filepath.Abs(tempRoot)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot temp directory: %w", err)
	}
	identity, err := snapshotUserIdentityFn()
	if err != nil {
		return "", fmt.Errorf("identify snapshot namespace owner: %w", err)
	}
	if identity == "" {
		return "", errors.New("identify snapshot namespace owner: empty identity")
	}
	digest := sha256.Sum256([]byte(identity))
	root := filepath.Join(abs, snapshotNamespacePrefix+fmt.Sprintf("%x", digest[:8]))
	if err := os.Mkdir(root, 0o700); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create snapshot namespace: %w", err)
	}
	before, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect snapshot namespace: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return "", fmt.Errorf("snapshot namespace %q is not a directory", root)
	}
	if !snapshotNamespaceOwned(root, before) {
		return "", fmt.Errorf("snapshot namespace %q has a different owner", root)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", fmt.Errorf("secure snapshot namespace: %w", err)
	}
	after, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("validate snapshot namespace: %w", err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !os.SameFile(before, after) ||
		!snapshotNamespaceOwned(root, after) || !snapshotNamespacePermissionsValid(after) {
		return "", fmt.Errorf("snapshot namespace %q failed validation", root)
	}
	return root, nil
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
		lease.mu.Lock()
		defer lease.mu.Unlock()
		var releaseErr error
		if lease.release != nil {
			releaseErr = lease.release()
		}
		lease.err = errors.Join(releaseErr, cleanupSnapshotDirectory(lease.directory, nil))
		unregisterHeldSnapshot(lease)
	})
	return errors.Join(cause, lease.err)
}

// -/ 3/7

// -- 4/7 HELPER · Orphan reaping --

func scavengeReadOnlySnapshots(ctx context.Context, root string) error {
	return scavengeSnapshotRoot(ctx, root)
}

func scavengeSnapshotRoot(ctx context.Context, root string) (resultErr error) {
	releaseNamespace, err := lockSnapshotNamespace(ctx, root)
	if err != nil {
		return fmt.Errorf("lock read-only snapshot namespace: %w", err)
	}
	released := false
	defer func() {
		if !released {
			resultErr = errors.Join(resultErr, releaseNamespace())
		}
	}()

	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("inspect read-only snapshot directory: %w", err)
	}
	var reaped int
	var reclaimed int64
	var reapErrors []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(err, errors.Join(reapErrors...))
		}
		path := filepath.Join(root, entry.Name())
		candidate := snapshotReapCandidate(entry)
		if !candidate {
			continue
		}
		release, orphan, err := snapshotOrphanLease(path)
		if err != nil {
			reapErrors = append(reapErrors, fmt.Errorf("inspect orphan snapshot %q: %w", path, err))
			continue
		}
		if !orphan {
			continue
		}
		claimed, err := claimSnapshotDirectoryFn(root, path)
		if err != nil {
			if release != nil {
				if releaseErr := release(); releaseErr != nil {
					reapErrors = append(reapErrors,
						fmt.Errorf("release orphan snapshot lease %q: %w", path, releaseErr))
				}
			}
			if !os.IsNotExist(err) {
				reapErrors = append(reapErrors, fmt.Errorf("claim orphan snapshot %q: %w", path, err))
			}
			continue
		}
		size, err := directorySize(ctx, claimed)
		if err != nil {
			if release != nil {
				err = errors.Join(err, release())
			}
			reapErrors = append(reapErrors, fmt.Errorf("measure orphan snapshot %q: %w", claimed, err))
			continue
		}
		if release != nil {
			if err := release(); err != nil {
				reapErrors = append(reapErrors, fmt.Errorf("release orphan snapshot lease %q: %w", claimed, err))
				continue
			}
		}
		if err := removeSnapshotDirectoryFn(claimed); err != nil {
			if !os.IsNotExist(err) {
				reapErrors = append(reapErrors, fmt.Errorf("remove orphan snapshot %q: %w", claimed, err))
			}
			continue
		}
		if _, err := os.Stat(claimed); err == nil {
			reapErrors = append(reapErrors, fmt.Errorf("remove orphan snapshot %q: directory still exists", claimed))
			continue
		} else if !os.IsNotExist(err) {
			reapErrors = append(reapErrors, fmt.Errorf("verify orphan snapshot removal %q: %w", claimed, err))
			continue
		}
		reaped++
		reclaimed += size
	}
	resultErr = errors.Join(errors.Join(reapErrors...), releaseNamespace())
	released = true
	if reaped > 0 {
		logSnapshotRecord(ctx, snapshotLogWriterFromContext(ctx), map[string]any{
			"event":           "reap",
			"count":           reaped,
			"reclaimed_mb":    reclaimed / bytesPerMB,
			"reclaimed_bytes": reclaimed,
		})
	}
	return resultErr
}

func snapshotReapCandidate(entry os.DirEntry) bool {
	return entry.IsDir() && (strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) ||
		strings.HasPrefix(entry.Name(), snapshotStagingPrefix) ||
		strings.HasPrefix(entry.Name(), snapshotReapPrefix))
}

func claimSnapshotDirectory(root, directory string) (string, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
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
			identity, err := snapshotFileIdentity(path+suffix, info)
			if err != nil {
				return snapshotSourceState{}, fmt.Errorf("identify snapshot source %q: %w", path+suffix, err)
			}
			state[index] = snapshotArtifactState{
				exists: true, size: info.Size(), modTime: info.ModTime().UnixNano(), identity: identity,
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
		fmt.Fprintf(&builder, "|%t|%d|%d|%s", artifact.exists, artifact.size, artifact.modTime, artifact.identity)
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

func directorySize(ctx context.Context, root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := snapshotEntryInfoFn(entry)
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func beginSnapshotCopy() *snapshotCopyInflight {
	inflight := &snapshotCopyInflight{done: make(chan struct{})}
	snapshotCopyMu.Lock()
	snapshotCopies[inflight] = struct{}{}
	snapshotCopyMu.Unlock()
	return inflight
}

func (inflight *snapshotCopyInflight) finish() {
	snapshotCopyMu.Lock()
	delete(snapshotCopies, inflight)
	snapshotCopyMu.Unlock()
	close(inflight.done)
}

func registerSnapshotCopyHandle(file *os.File) {
	snapshotCopyMu.Lock()
	snapshotCopyHandles[file] = struct{}{}
	snapshotCopyMu.Unlock()
}

func unregisterSnapshotCopyHandle(file *os.File) {
	snapshotCopyMu.Lock()
	delete(snapshotCopyHandles, file)
	snapshotCopyMu.Unlock()
}

func drainSnapshotCopies() {
	snapshotCopyMu.Lock()
	handles := make([]*os.File, 0, len(snapshotCopyHandles))
	for file := range snapshotCopyHandles {
		handles = append(handles, file)
	}
	copies := make([]*snapshotCopyInflight, 0, len(snapshotCopies))
	for inflight := range snapshotCopies {
		copies = append(copies, inflight)
	}
	snapshotCopyMu.Unlock()
	for _, file := range handles {
		_ = file.Close()
	}
	for _, inflight := range copies {
		<-inflight.done
	}
}

func copySnapshotSource(ctx context.Context, source, destination string) error {
	inflight := beginSnapshotCopy()
	defer inflight.finish()

	copyCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(snapshotShutdownCtx, cancel)
	defer func() {
		stop()
		cancel()
	}()

	for _, suffix := range snapshotArtifacts {
		if err := copyCtx.Err(); err != nil {
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
		registerSnapshotCopyHandle(output)
		if snapshotAfterCopyHandleFn != nil {
			snapshotAfterCopyHandleFn(destination + suffix)
		}
		_, copyErr := io.CopyBuffer(output, &contextReader{ctx: copyCtx, reader: input},
			make([]byte, snapshotCopyBufferSize))
		err = errors.Join(copyErr, copyCtx.Err(), input.Close(), output.Close())
		unregisterSnapshotCopyHandle(output)
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
	cached := snapshotCache[artifact.fingerprint] == artifact
	destroy := artifact.refs == 0 && !cached
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
				cleanupHeldSnapshots()
				close(cleaned)
			}()
			timer := time.NewTimer(snapshotSignalCleanupLimit)
			select {
			case <-cleaned:
				timer.Stop()
			case <-timer.C:
			}
			terminateSnapshotProcess(sig)
		}()
	})
}

func beginSnapshotShutdown() {
	snapshotHeldMu.Lock()
	snapshotShuttingDown.Store(true)
	snapshotHeldMu.Unlock()
	snapshotShutdownCancel()
}

func CloseReadOnlySnapshots() error {
	return cleanupHeldSnapshots()
}

func cleanupHeldSnapshots() error {
	drainSnapshotCopies()

	snapshotCacheMu.Lock()
	live := make([]*snapshotArtifact, 0, len(snapshotCache))
	for _, artifact := range snapshotCache {
		artifact.refs = 0
		live = append(live, artifact)
	}
	snapshotCache = map[string]*snapshotArtifact{}
	snapshotCacheMu.Unlock()
	var result error
	for _, artifact := range live {
		result = errors.Join(result, artifact.destroy())
	}

	snapshotHeldMu.Lock()
	pending := snapshotHeld
	snapshotHeld = map[*snapshotLease]struct{}{}
	snapshotHeldMu.Unlock()
	for lease := range pending {
		result = errors.Join(result, lease.destroy(nil))
	}
	return result
}

// -/ 6/7

// -- 7/7 HELPER · JSONL telemetry --

func snapshotLogWriterFromContext(ctx context.Context) SnapshotLogWriter {
	writer, _ := ctx.Value(snapshotLogContextKey{}).(SnapshotLogWriter)
	return writer
}

func snapshotCoordinationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout, _ := ctx.Value(snapshotCoordinationTimeoutContextKey{}).(time.Duration)
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

func logSnapshotRecord(ctx context.Context, writer SnapshotLogWriter, record map[string]any) {
	if writer == nil {
		return
	}
	record["timestamp"] = time.Now().UTC()
	_ = writer(ctx, record)
}

// -/ 7/7
