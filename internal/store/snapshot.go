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
	"syscall"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

var snapshotArtifacts = [...]string{"", "-wal", "-journal"}

const (
	snapshotDirectoryPrefix = "roca-read-only-snapshot-"
	snapshotLeaseName       = "lease"
	snapshotCopyBufferSize  = 128 * 1024
	snapshotLogStream       = "snapshots"
	bytesPerMB              = 1024 * 1024
)

type ReadOnlySnapshot struct {
	database    *sql.DB
	uri         string
	directory   string
	lease       func() error
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

var (
	snapshotLogDir atomic.Pointer[string]

	snapshotCacheMu sync.Mutex
	snapshotCache   = map[string]*ReadOnlySnapshot{}
	snapshotFlight  = map[string]*snapshotInflight{}

	snapshotHeldMu sync.Mutex
	snapshotHeld   = map[string]func() error{}

	snapshotExitOnce sync.Once
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

func OpenReadOnlySnapshot(ctx context.Context, path string) (*ReadOnlySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	ensureSnapshotExitCleanup()
	if err := scavengeReadOnlySnapshots(ctx, os.TempDir()); err != nil {
		return nil, err
	}
	before, err := inspectSnapshotSource(abs)
	if err != nil {
		return nil, err
	}
	fingerprint := snapshotFingerprint(abs, before)

	snapshotCacheMu.Lock()
	if cached, ok := snapshotCache[fingerprint]; ok {
		cached.refs++
		snapshotCacheMu.Unlock()
		return cached, nil
	}
	if flight, ok := snapshotFlight[fingerprint]; ok {
		snapshotCacheMu.Unlock()
		<-flight.ready
		if flight.err != nil {
			return nil, flight.err
		}
		snapshotCacheMu.Lock()
		if cached, ok := snapshotCache[fingerprint]; ok {
			cached.refs++
			snapshotCacheMu.Unlock()
			return cached, nil
		}
		snapshotCacheMu.Unlock()
		return OpenReadOnlySnapshot(ctx, path)
	}
	flight := &snapshotInflight{ready: make(chan struct{})}
	snapshotFlight[fingerprint] = flight
	snapshotCacheMu.Unlock()

	snapshot, err := createReadOnlySnapshot(ctx, abs, before)
	snapshotCacheMu.Lock()
	delete(snapshotFlight, fingerprint)
	if err == nil {
		snapshot.fingerprint = fingerprint
		snapshot.refs = 1
		snapshotCache[fingerprint] = snapshot
	}
	flight.err = err
	close(flight.ready)
	snapshotCacheMu.Unlock()
	return snapshot, err
}

func createReadOnlySnapshot(ctx context.Context, abs string, before snapshotSourceState) (*ReadOnlySnapshot, error) {
	directory, err := os.MkdirTemp(os.TempDir(), snapshotDirectoryPrefix)
	if err != nil {
		return nil, fmt.Errorf("create read-only snapshot directory: %w", err)
	}
	lease, err := securefile.Lock(filepath.Join(directory, snapshotLeaseName))
	if err != nil {
		return nil, cleanupSnapshotDirectory(directory, fmt.Errorf("lock read-only snapshot: %w", err))
	}
	_ = os.WriteFile(filepath.Join(directory, snapshotLeaseName),
		[]byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	registerHeldSnapshot(directory, lease)
	abandon := func(cause error) error {
		return abandonSnapshotDirectory(directory, lease, cause)
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
		if err := copySnapshotSource(ctx, abs, destination); err != nil {
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

func scavengeReadOnlySnapshots(ctx context.Context, root string) error {
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
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), snapshotDirectoryPrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		orphan, err := snapshotLeaseOrphan(path)
		if err != nil || !orphan {
			continue
		}
		size := directorySize(path)
		if err := os.RemoveAll(path); err != nil {
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

func snapshotLeaseOrphan(directory string) (bool, error) {
	release, err := securefile.TryLock(filepath.Join(directory, snapshotLeaseName))
	if err == nil {
		_ = release()
		return true, nil
	}
	if os.IsNotExist(err) {
		return true, nil
	}
	if errors.Is(err, securefile.ErrBusy) {
		return false, nil
	}
	return false, err
}

func cleanupSnapshotDirectory(directory string, cause error) error {
	if err := os.RemoveAll(directory); err != nil {
		return errors.Join(cause, fmt.Errorf("remove read-only snapshot %q: %w", directory, err))
	}
	return cause
}

func abandonSnapshotDirectory(directory string, lease func() error, cause error) error {
	unregisterHeldSnapshot(directory)
	var leaseErr error
	if lease != nil {
		leaseErr = lease()
	}
	return errors.Join(cause, leaseErr, cleanupSnapshotDirectory(directory, nil))
}

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

func openCopiedSnapshot(ctx context.Context, path, directory string) (*ReadOnlySnapshot, error) {
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
	return &ReadOnlySnapshot{database: database, uri: uri, directory: directory}, nil
}

func (snapshot *ReadOnlySnapshot) SQL() *sql.DB {
	return snapshot.database
}

func (snapshot *ReadOnlySnapshot) URI() string {
	return snapshot.uri
}

func (snapshot *ReadOnlySnapshot) Close() error {
	if snapshot == nil {
		return nil
	}
	snapshotCacheMu.Lock()
	if snapshot.refs > 1 {
		snapshot.refs--
		snapshotCacheMu.Unlock()
		return nil
	}
	if snapshot.refs == 1 {
		snapshot.refs = 0
		delete(snapshotCache, snapshot.fingerprint)
	}
	snapshotCacheMu.Unlock()
	return snapshot.destroy()
}

func (snapshot *ReadOnlySnapshot) destroy() error {
	snapshot.once.Do(func() {
		var closeErr error
		if snapshot.database != nil {
			closeErr = snapshot.database.Close()
		}
		snapshot.err = abandonSnapshotDirectory(snapshot.directory, snapshot.lease, closeErr)
	})
	return snapshot.err
}

func registerHeldSnapshot(directory string, lease func() error) {
	snapshotHeldMu.Lock()
	snapshotHeld[directory] = lease
	snapshotHeldMu.Unlock()
}

func unregisterHeldSnapshot(directory string) {
	snapshotHeldMu.Lock()
	delete(snapshotHeld, directory)
	snapshotHeldMu.Unlock()
}

func ensureSnapshotExitCleanup() {
	snapshotExitOnce.Do(func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		go func() {
			sig := <-signals
			signal.Stop(signals)
			cleanupHeldSnapshots()
			signal.Reset(os.Interrupt, syscall.SIGTERM)
			if process, err := os.FindProcess(os.Getpid()); err == nil {
				_ = process.Signal(sig)
			}
		}()
	})
}

func cleanupHeldSnapshots() {
	snapshotCacheMu.Lock()
	live := make([]*ReadOnlySnapshot, 0, len(snapshotCache))
	for _, snapshot := range snapshotCache {
		live = append(live, snapshot)
	}
	snapshotCache = map[string]*ReadOnlySnapshot{}
	snapshotCacheMu.Unlock()
	for _, snapshot := range live {
		_ = snapshot.destroy()
	}

	snapshotHeldMu.Lock()
	pending := snapshotHeld
	snapshotHeld = map[string]func() error{}
	snapshotHeldMu.Unlock()
	for directory, lease := range pending {
		if lease != nil {
			_ = lease()
		}
		_ = os.RemoveAll(directory)
	}
}

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
