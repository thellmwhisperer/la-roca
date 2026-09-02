package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
	workerActivityFile   = ".worker-status.json"
	sourceMarkerMetaKey  = "source_marker"
)

var (
	candidateCountTimeout = 500 * time.Millisecond
	countDeclaredChunks   = readDeclaredChunkCount
	statVectorFile        = os.Stat
	workerActivityMu      sync.Mutex
)

// StatusRequest is the filesystem seat status reads. It does not take an
// embedder: asking how the index is doing must not wait for the model.
type StatusRequest struct {
	PluginRoot string
	StateDir   string
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
		Worker:    readWorkerStatus(req.StateDir),
		Databases: make([]DatabaseVectorization, len(registry.Databases)),
	}
	var wg sync.WaitGroup
	for index, database := range registry.Databases {
		wg.Add(1)
		go func(index int, database vectorDatabase) {
			defer wg.Done()
			active := report.Worker.Database != nil && *report.Worker.Database == database.owner()
			report.Databases[index] = inspectDatabase(ctx, pluginRoot, database, active)
		}(index, database)
	}
	wg.Wait()
	return report, nil
}

func readWorkerStatus(stateDir string) WorkerStatus {
	status := WorkerStatus{}
	if stateDir == "" {
		return status
	}
	claim, running := liveWorkerClaim(stateDir)
	if !running {
		return status
	}
	status.Running = true
	status.PID = &claim.PID
	activity, err := readWorkerActivity(stateDir)
	if err != nil || activity.PID != claim.PID || activity.RunID == "" || activity.RunID != claim.RunID {
		return status
	}
	backend := strings.ToLower(strings.TrimSpace(activity.Backend))
	if backend == "cpu" || backend == "metal" {
		status.Backend = &backend
	}
	if database := strings.TrimSpace(activity.Database); database != "" {
		status.Database = &database
	}
	return status
}

func inspectDatabase(ctx context.Context, pluginRoot string, database vectorDatabase, workerActive bool) DatabaseVectorization {
	row := DatabaseVectorization{
		Plugin:   database.Plugin,
		Database: database.Database,
		Tables:   declaredTableNames(database),
		State:    StateUnknown,
	}
	sourcePath := filepath.Join(pluginRoot, database.Plugin, database.Path)
	sidecarPath := SidecarPath(sourcePath)
	bytes, lastWrite, exists, statErr := sidecarFileFacts(sidecarPath)
	row.SidecarBytes, row.LastWrite = bytes, lastWrite
	candidate := boundedCandidateCount(ctx, database, sourcePath)
	if statErr != nil {
		return row
	}
	if !exists {
		row.CandidateChunks = candidateCountForMarker(candidate, currentSourceMarker(sourcePath))
		row.State = StateEmpty
		return row
	}
	store, err := openSQLiteBusy(sidecarPath, true, statusBusyTimeoutMS)
	if err != nil {
		return row
	}
	defer store.Close()
	snapshot, readable := readSidecarSnapshot(ctx, store)
	if !readable {
		return row
	}
	row.EmbeddedChunks = &snapshot.EmbeddedChunks
	marker := currentSourceMarker(sourcePath)
	row.CandidateChunks = candidateCountForMarker(candidate, marker)
	row.State = classifySidecar(exists, true, workerActive, row.EmbeddedChunks, snapshot.Contract,
		database.contractFingerprint(), snapshot.Fingerprint, snapshot.SourceMarker, marker)
	return row
}

type sidecarSnapshot struct {
	EmbeddedChunks int64
	Contract       string
	Fingerprint    string
	SourceMarker   string
}

func readSidecarSnapshot(ctx context.Context, store *sql.DB) (sidecarSnapshot, bool) {
	snapshotCtx, cancel := boundContext(ctx, statusCountTimeout)
	defer cancel()
	tx, err := store.BeginTx(snapshotCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return sidecarSnapshot{}, false
	}
	defer tx.Rollback()
	var snapshot sidecarSnapshot
	if err := tx.QueryRowContext(snapshotCtx, `SELECT COUNT(*) FROM chunks`).Scan(&snapshot.EmbeddedChunks); err != nil {
		return sidecarSnapshot{}, false
	}
	rows, err := tx.QueryContext(snapshotCtx, `SELECT key,value FROM meta WHERE key IN (?,?,?)`,
		"contract", "source_fingerprint", sourceMarkerMetaKey)
	if err != nil {
		return sidecarSnapshot{}, false
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return sidecarSnapshot{}, false
		}
		switch key {
		case "contract":
			snapshot.Contract = value
		case "source_fingerprint":
			snapshot.Fingerprint = value
		case sourceMarkerMetaKey:
			snapshot.SourceMarker = value
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return sidecarSnapshot{}, false
	}
	if err := rows.Close(); err != nil {
		return sidecarSnapshot{}, false
	}
	if err := tx.Commit(); err != nil {
		return sidecarSnapshot{}, false
	}
	return snapshot, true
}

func classifySidecar(exists, readable, workerActive bool, chunks *int64, storedContract,
	currentContract, fingerprint, storedMarker string, currentMarker *string) string {
	if exists && !readable {
		return StateUnknown
	}
	if !exists {
		return StateEmpty
	}
	if storedContract != "" && currentContract != "" && storedContract != currentContract {
		return StateOutdated
	}
	if fingerprint != "" {
		if storedContract == "" || currentContract == "" || storedContract != currentContract ||
			storedMarker == "" || currentMarker == nil {
			return StateUnknown
		}
		if storedMarker != *currentMarker {
			return StateOutdated
		}
		return StateComplete
	}
	if workerActive {
		return StateBuilding
	}
	if chunks != nil && *chunks == 0 {
		return StateEmpty
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

func sidecarFileFacts(path string) (*int64, *string, bool, error) {
	facts, err := stableSQLiteFileFacts(path, []string{"", "-wal", "-shm"})
	if err != nil {
		return nil, nil, false, err
	}
	if !facts[0].Exists {
		return nil, nil, false, nil
	}
	size := facts[0].Size
	mtime := facts[0].ModTime
	for _, fact := range facts[1:] {
		if !fact.Exists {
			continue
		}
		size += fact.Size
		if fact.ModTime > mtime {
			mtime = fact.ModTime
		}
	}
	stamp := time.Unix(0, mtime).UTC().Format(time.RFC3339)
	return &size, &stamp, true, nil
}

type candidateChunkSnapshot struct {
	Chunks       *int64
	SourceMarker string
}

func boundedCandidateCount(ctx context.Context, database vectorDatabase, path string) candidateChunkSnapshot {
	ctx, cancel := boundContext(ctx, candidateCountTimeout)
	defer cancel()
	type reply struct {
		snapshot candidateChunkSnapshot
		err      error
	}
	ch := make(chan reply, 1)
	go func() {
		snapshot, err := countDeclaredChunks(ctx, database, path)
		ch <- reply{snapshot, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			return candidateChunkSnapshot{}
		}
		return got.snapshot
	case <-ctx.Done():
		return candidateChunkSnapshot{}
	}
}

func readDeclaredChunkCount(ctx context.Context, database vectorDatabase, path string) (candidateChunkSnapshot, error) {
	before, err := sourceFileMarker(path)
	if os.IsNotExist(err) {
		return candidateChunkSnapshot{}, nil
	} else if err != nil {
		return candidateChunkSnapshot{}, err
	}
	store, err := openSQLiteBusy(path, true, int(candidateCountTimeout/time.Millisecond))
	if err != nil {
		return candidateChunkSnapshot{}, err
	}
	defer store.Close()
	tx, err := store.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return candidateChunkSnapshot{}, err
	}
	defer tx.Rollback()
	var total int64
	for _, table := range database.Tables {
		columns := make([]string, len(table.TextColumns))
		for index, column := range table.TextColumns {
			columns[index] = `COALESCE(CAST(` + quoteIdentifier(column) + ` AS TEXT),'')`
		}
		statement := `SELECT ` + strings.Join(columns, ",") + ` FROM ` + quoteIdentifier(table.Name) +
			` WHERE ` + declaredSourcePredicate("", table)
		rows, err := tx.QueryContext(ctx, statement)
		if err != nil {
			return candidateChunkSnapshot{}, err
		}
		for rows.Next() {
			values := make([]string, len(columns))
			targets := make([]any, len(columns))
			for index := range values {
				targets[index] = &values[index]
			}
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				return candidateChunkSnapshot{}, err
			}
			for _, value := range values {
				text := strings.TrimSpace(value)
				if table.Chunking != nil &&
					(table.Chunking.MaxChars != nil || table.Chunking.OverlapChars != nil) {
					size, overlap := table.chunking()
					total += int64(len(chunks(text, size, overlap)))
				} else {
					total += int64(len(tokenChunks(text, defaultChunkTokens, defaultOverlapTokens)))
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return candidateChunkSnapshot{}, err
		}
		if err := rows.Close(); err != nil {
			return candidateChunkSnapshot{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return candidateChunkSnapshot{}, err
	}
	after, err := sourceFileMarker(path)
	if err != nil {
		return candidateChunkSnapshot{}, err
	}
	if before != after {
		return candidateChunkSnapshot{}, errSourceChanged
	}
	return candidateChunkSnapshot{Chunks: &total, SourceMarker: after}, nil
}

func currentSourceMarker(path string) *string {
	marker, err := sourceFileMarker(path)
	if err == nil {
		return &marker
	}
	if os.IsNotExist(err) {
		missing := "missing"
		return &missing
	}
	return nil
}

func candidateCountForMarker(candidate candidateChunkSnapshot, marker *string) *int64 {
	if candidate.Chunks == nil || marker == nil || candidate.SourceMarker != *marker {
		return nil
	}
	return candidate.Chunks
}

type workerActivity struct {
	PID      int    `json:"pid"`
	RunID    string `json:"run_id"`
	Backend  string `json:"backend,omitempty"`
	Database string `json:"database,omitempty"`
}

func readWorkerActivity(stateDir string) (workerActivity, error) {
	raw, err := os.ReadFile(filepath.Join(stateDir, workerActivityFile))
	if err != nil {
		return workerActivity{}, err
	}
	var activity workerActivity
	if err := json.Unmarshal(raw, &activity); err != nil {
		return workerActivity{}, err
	}
	return activity, nil
}

func updateWorkerActivity(stateDir, backend string, database *string) error {
	if stateDir == "" {
		return nil
	}
	claim, err := readWorkerClaim(stateDir)
	if err != nil || claim.PID != os.Getpid() || claim.RunID == "" {
		return nil
	}
	workerActivityMu.Lock()
	defer workerActivityMu.Unlock()
	activity, _ := readWorkerActivity(stateDir)
	if activity.PID != os.Getpid() || activity.RunID != claim.RunID {
		activity = workerActivity{PID: os.Getpid(), RunID: claim.RunID}
	}
	if backend != "" {
		activity.Backend = backend
	}
	if database != nil {
		activity.Database = *database
	}
	raw, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	temporary := filepath.Join(stateDir, ".worker-status.tmp")
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(stateDir, workerActivityFile))
}

func clearWorkerActivity(stateDir string) error {
	workerActivityMu.Lock()
	defer workerActivityMu.Unlock()
	err := os.Remove(filepath.Join(stateDir, workerActivityFile))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

type sourceMarkerFact struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mtime_ns"`
}

func sourceFileMarker(path string) (string, error) {
	suffixes := []string{"", "-wal"}
	fileFacts, err := stableSQLiteFileFacts(path, suffixes)
	if err != nil {
		return "", err
	}
	if !fileFacts[0].Exists {
		return "", &os.PathError{Op: "stat", Path: path, Err: os.ErrNotExist}
	}
	facts := make([]sourceMarkerFact, 0, len(fileFacts))
	for index, fact := range fileFacts {
		if !fact.Exists {
			continue
		}
		candidate := path + suffixes[index]
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", err
		}
		facts = append(facts, sourceMarkerFact{Path: filepath.Clean(absolute), Size: fact.Size,
			ModTime: fact.ModTime})
	}
	raw, err := json.Marshal(facts)
	return string(raw), err
}

type sqliteFileFact struct {
	Exists  bool
	Size    int64
	ModTime int64
}

func stableSQLiteFileFacts(path string, suffixes []string) ([]sqliteFileFact, error) {
	before, err := readSQLiteFileFacts(path, suffixes)
	if err != nil {
		return nil, err
	}
	after, err := readSQLiteFileFacts(path, suffixes)
	if err != nil {
		return nil, err
	}
	if len(before) != len(after) {
		return nil, errSourceChanged
	}
	for index := range before {
		if before[index] != after[index] {
			return nil, errSourceChanged
		}
	}
	return after, nil
}

func readSQLiteFileFacts(path string, suffixes []string) ([]sqliteFileFact, error) {
	facts := make([]sqliteFileFact, len(suffixes))
	for index, suffix := range suffixes {
		info, err := statVectorFile(path + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		facts[index] = sqliteFileFact{Exists: true, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	}
	return facts, nil
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
