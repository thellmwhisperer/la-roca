package vector

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/telemetry"
)

const (
	StateBuilding = "building"
	StateComplete = "complete"
	StateEmpty    = "empty"
	StateOutdated = "outdated"
	StateUnknown  = "unknown"

	statusBusyTimeoutMS  = 250
	statusCountTimeout   = time.Second
	statusOverallTimeout = 3 * time.Second
)

var (
	candidateCountTimeout = 500 * time.Millisecond
	countDeclaredSources  = readDeclaredSourceCount
)

// StatusRequest is the filesystem seat status reads. It does not take an
// embedder: asking how the index is doing must not wait for the model.
type StatusRequest struct {
	PluginRoot string
	StateDir   string
	DataDir    string
}

type Vectorization struct {
	Worker    WorkerStatus            `json:"worker"`
	Databases []DatabaseVectorization `json:"databases"`
}

type WorkerStatus struct {
	Running  bool    `json:"running"`
	PID      *int    `json:"pid"`
	Backend  *string `json:"backend"`
	Database *string `json:"database"`
}

type DatabaseVectorization struct {
	Plugin          string   `json:"plugin"`
	Database        string   `json:"database"`
	Tables          []string `json:"tables"`
	EmbeddedChunks  *int64   `json:"embedded_chunks"`
	CandidateChunks *int64   `json:"candidate_chunks"`
	SidecarBytes    *int64   `json:"sidecar_bytes"`
	LastWrite       *string  `json:"last_write"`
	State           string   `json:"state"`
}

func ReportVectorization(ctx context.Context, req StatusRequest) (Vectorization, error) {
	ctx, cancel := boundContext(ctx, statusOverallTimeout)
	defer cancel()
	pluginRoot, err := filepath.Abs(req.PluginRoot)
	if err != nil {
		return Vectorization{}, err
	}
	registry, err := loadVectorRegistry(filepath.Join(pluginRoot, vectorRegistryFilename))
	if err != nil {
		return Vectorization{}, err
	}
	report := Vectorization{
		Worker:    readWorkerStatus(req.StateDir, req.DataDir),
		Databases: make([]DatabaseVectorization, len(registry.Databases)),
	}
	var wg sync.WaitGroup
	for index, database := range registry.Databases {
		wg.Add(1)
		go func(index int, database vectorDatabase) {
			defer wg.Done()
			report.Databases[index] = inspectDatabase(ctx, pluginRoot, database, report.Worker.Running)
		}(index, database)
	}
	wg.Wait()
	return report, nil
}

func readWorkerStatus(stateDir, dataDir string) WorkerStatus {
	status := WorkerStatus{}
	if stateDir == "" {
		status.Backend = latestWriterBackend(dataDir)
		return status
	}
	status.Running = WorkerRunning(stateDir)
	if status.Running {
		if pid := ReadWorkerPID(stateDir); pid > 0 {
			status.PID = &pid
		}
	}
	status.Backend = latestWriterBackend(dataDir)
	return status
}

func inspectDatabase(ctx context.Context, pluginRoot string, database vectorDatabase, workerRunning bool) DatabaseVectorization {
	row := DatabaseVectorization{
		Plugin:   database.Plugin,
		Database: database.Database,
		Tables:   declaredTableNames(database),
		State:    StateUnknown,
	}
	sourcePath := filepath.Join(pluginRoot, database.Plugin, database.Path)
	sidecarPath := SidecarPath(sourcePath)
	bytes, lastWrite, exists := sidecarFileFacts(sidecarPath)
	row.SidecarBytes, row.LastWrite = bytes, lastWrite
	row.CandidateChunks = boundedCandidateCount(ctx, database, sourcePath)
	if !exists {
		row.State = StateEmpty
		return row
	}
	store, err := openSQLiteBusy(sidecarPath, true, statusBusyTimeoutMS)
	if err != nil {
		row.State = StateUnknown
		return row
	}
	defer store.Close()
	row.EmbeddedChunks = readEmbeddedChunks(ctx, store, sidecarPath)
	contract := optionalMeta(store, "contract")
	fingerprint := optionalMeta(store, "source_fingerprint")
	row.State = classifySidecar(exists, true, workerRunning, row.EmbeddedChunks, contract,
		database.contractFingerprint(), fingerprint)
	return row
}

func readEmbeddedChunks(ctx context.Context, store *sql.DB, sidecarPath string) *int64 {
	countCtx, cancel := boundContext(ctx, statusCountTimeout)
	defer cancel()
	var chunks int64
	if err := store.QueryRowContext(countCtx, `SELECT COUNT(*) FROM chunks`).Scan(&chunks); err == nil {
		return &chunks
	}
	// COUNT on a multi-gigabyte sidecar scans the whole btree and would block
	// status. MAX(id) is the highest stored chunk identity and is O(log n).
	maxCtx, cancelMax := boundContext(ctx, 200*time.Millisecond)
	defer cancelMax()
	fallback, err := openSQLiteBusy(sidecarPath, true, statusBusyTimeoutMS)
	if err != nil {
		return nil
	}
	defer fallback.Close()
	var maxID sql.NullInt64
	if err := fallback.QueryRowContext(maxCtx, `SELECT MAX(id) FROM chunks`).Scan(&maxID); err != nil || !maxID.Valid {
		return nil
	}
	return &maxID.Int64
}

func classifySidecar(exists, readable, workerRunning bool, chunks *int64, storedContract, currentContract, fingerprint string) string {
	if exists && !readable {
		return StateUnknown
	}
	if !exists {
		return StateEmpty
	}
	if storedContract != "" && currentContract != "" && storedContract != currentContract {
		return StateOutdated
	}
	if fingerprint != "" && (storedContract == "" || storedContract == currentContract) {
		return StateComplete
	}
	if chunks != nil && *chunks == 0 && fingerprint == "" {
		return StateEmpty
	}
	if fingerprint == "" && (workerRunning || (chunks != nil && *chunks > 0)) {
		return StateBuilding
	}
	return StateUnknown
}

func declaredTableNames(database vectorDatabase) []string {
	names := make([]string, 0, len(database.Tables))
	for _, table := range database.Tables {
		names = append(names, table.Name)
	}
	return names
}

func sidecarFileFacts(path string) (*int64, *string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, false
	}
	size := info.Size()
	mtime := info.ModTime()
	for _, extra := range []string{path + "-wal", path + "-shm"} {
		extraInfo, extraErr := os.Stat(extra)
		if extraErr != nil {
			continue
		}
		size += extraInfo.Size()
		if extraInfo.ModTime().After(mtime) {
			mtime = extraInfo.ModTime()
		}
	}
	stamp := mtime.UTC().Format(time.RFC3339)
	return &size, &stamp, true
}

func optionalMeta(store *sql.DB, key string) string {
	var value string
	if err := store.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value); err != nil {
		return ""
	}
	return value
}

func boundedCandidateCount(ctx context.Context, database vectorDatabase, path string) *int64 {
	ctx, cancel := boundContext(ctx, candidateCountTimeout)
	defer cancel()
	type reply struct {
		n   *int64
		err error
	}
	ch := make(chan reply, 1)
	go func() {
		n, err := countDeclaredSources(ctx, database, path)
		ch <- reply{n, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			return nil
		}
		return got.n
	case <-ctx.Done():
		return nil
	}
}

func readDeclaredSourceCount(ctx context.Context, database vectorDatabase, path string) (*int64, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	store, err := openSQLiteBusy(path, true, int(candidateCountTimeout/time.Millisecond))
	if err != nil {
		return nil, err
	}
	defer store.Close()
	var total int64
	for _, table := range database.Tables {
		statement := `SELECT COUNT(*) FROM ` + quoteIdentifier(table.Name) + ` WHERE ` +
			declaredSourcePredicate("", table)
		var count int64
		if err := store.QueryRowContext(ctx, statement).Scan(&count); err != nil {
			return nil, err
		}
		total += count
	}
	return &total, nil
}

func latestWriterBackend(dataDir string) *string {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(telemetry.Dir(dataDir), telemetry.Stream+"-*.jsonl"))
	if err != nil || len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	file, err := os.Open(matches[len(matches)-1])
	if err != nil {
		return nil
	}
	defer file.Close()
	var backend string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var record telemetry.Record
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(record.Backend)) {
		case "metal", "cpu":
			backend = strings.ToLower(record.Backend)
		}
	}
	if backend == "" {
		return nil
	}
	return &backend
}

func openSQLiteBusy(path string, readOnly bool, busyMS int) (*sql.DB, error) {
	if sourceFingerprintRegistrationErr != nil {
		return nil, sourceFingerprintRegistrationErr
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if busyMS < 0 {
		busyMS = 0
	}
	values := url.Values{"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyMS)}}
	if readOnly {
		values.Set("mode", "ro")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: values.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
