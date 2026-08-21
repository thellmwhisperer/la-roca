// Package vector owns the optional local semantic-search index.
package vector

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const (
	defaultChunkSize    = 4000
	defaultOverlap      = 400
	defaultBatchSize    = 64
	walkPageSize        = 500
	vectorStorageSchema = "vector-v1"
	// maxUnresolvedCandidates bounds a query against an index the corpus has moved
	// under. Each resolution is one `roca exec` process, so a wholly stale index
	// would otherwise spend one process per candidate to answer nothing.
	maxUnresolvedCandidates = 32
)

type Index struct {
	Corpus      Corpus
	VectorPath  string
	Model       string
	Embedder    Embedder
	Notice      func(string)
	SourceKinds map[string]bool
	Database    string
}

type Corpus interface {
	WalkSources(context.Context, string, func(sourceRow) error) error
	ResolveSource(context.Context, string, locator) (string, error)
}

type Delta struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
	Sources   int `json:"sources"`
	Chunks    int `json:"chunks"`
}

type Result struct {
	Rank     int     `json:"rank"`
	Score    float64 `json:"score"`
	Database string  `json:"database,omitempty"`
	Table    string  `json:"table,omitempty"`
	ID       string  `json:"id,omitempty"`
	Source   string  `json:"source"`
	SourceID string  `json:"source_id"`
	Text     string  `json:"text"`
}

type sourceRow struct {
	kind               string
	sourceID           string
	text               string
	chunkSize          int
	overlap            int
	fingerprintVersion string
	sessionID          string
	ordinal            int64
	hasOrdinal         bool
	position           string
	cronSource         string
	filePath           string
	layer              string
	origin             string
	createdAt          string
}

type locator struct {
	SourceID   string `json:"source_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Ordinal    int64  `json:"ordinal,omitempty"`
	HasOrdinal bool   `json:"has_ordinal,omitempty"`
	Position   string `json:"position,omitempty"`
	CronSource string `json:"cron_source,omitempty"`
	FilePath   string `json:"file_path,omitempty"`
	Layer      string `json:"layer,omitempty"`
	Origin     string `json:"origin,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	Identity   string `json:"identity,omitempty"`
}

type desiredChunk struct {
	sourceKind  string
	sourceID    string
	index       int
	fingerprint string
	locator     locator
	text        string
}

type storedChunk struct {
	id          int64
	sourceKind  string
	fingerprint string
}

const sessionEmbeddingTextVersion = "sessions-human-v2"

func ConfiguredModel(path string) string {
	db, err := openSQLite(path, true)
	if err != nil {
		return DefaultModel
	}
	defer db.Close()
	var model string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='model'`).Scan(&model); err != nil || model == "" {
		return DefaultModel
	}
	return model
}

func (i Index) Ingest(ctx context.Context) (Delta, error) {
	return i.ingest(ctx, "")
}

func (i Index) IngestSource(ctx context.Context, sourceKind string) (Delta, error) {
	if sourceKind == "" {
		return Delta{}, fmt.Errorf("source kind is required")
	}
	return i.ingest(ctx, sourceKind)
}

func (i Index) ingest(ctx context.Context, sourceKind string) (Delta, error) {
	if err := i.validate(); err != nil {
		return Delta{}, err
	}
	if err := validateSourceKind(sourceKind, i.SourceKinds); err != nil {
		return Delta{}, err
	}
	if err := ensureParent(i.VectorPath); err != nil {
		return Delta{}, err
	}
	release, err := lockIndex(ctx, i.VectorPath+".index.lock", i.waitingForIndex)
	if err != nil {
		return Delta{}, fmt.Errorf("lock vector index: %w", err)
	}
	defer release()

	store, err := openSQLite(i.VectorPath, false)
	if err != nil {
		return Delta{}, fmt.Errorf("open vector database: %w", err)
	}
	defer store.Close()
	if err := ensureBaseSchema(store); err != nil {
		return Delta{}, err
	}
	existing, model, dimensions, err := readIndexState(store)
	if err != nil {
		return Delta{}, err
	}
	if model != "" && model != i.Model {
		if sourceKind != "" {
			return Delta{}, fmt.Errorf("targeted vector ingest cannot change model from %s to %s", model, i.Model)
		}
		if err := resetIndex(store); err != nil {
			return Delta{}, err
		}
		existing, dimensions = map[string]storedChunk{}, 0
	}
	if dimensions > 0 && model != i.Model {
		if err := ensureVectorTables(store, dimensions, i.Model); err != nil {
			return Delta{}, err
		}
	}

	report := Delta{}
	desiredFingerprints := make(map[string]string, len(existing))
	pending := make([]desiredChunk, 0, defaultBatchSize)
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		input := make([]string, len(pending))
		for n := range pending {
			input[n] = DocumentPrefix + pending[n].text
		}
		vectors, err := i.Embedder.Embed(ctx, i.Model, input)
		if err != nil {
			return err
		}
		if len(vectors) == 0 || len(vectors[0]) == 0 {
			return fmt.Errorf("embedding model %s returned an empty vector", i.Model)
		}
		if dimensions == 0 {
			dimensions = len(vectors[0])
			if err := ensureVectorTables(store, dimensions, i.Model); err != nil {
				return err
			}
		}
		for n := range vectors {
			if len(vectors[n]) != dimensions {
				return fmt.Errorf("embedding %d has %d dimensions, want %d", n, len(vectors[n]), dimensions)
			}
		}
		if err := writeBatch(ctx, store, pending, vectors, existing, &report); err != nil {
			return err
		}
		pending = pending[:0]
		return nil
	}

	err = i.Corpus.WalkSources(ctx, sourceKind, func(source sourceRow) error {
		report.Sources++
		chunkSize, overlap := source.chunking()
		for chunkIndex, text := range chunks(source.text, chunkSize, overlap) {
			chunk := desiredChunk{
				sourceKind: source.kind, sourceID: source.stableID(), index: chunkIndex,
				fingerprint: source.embeddingFingerprint(text), locator: source.locator(), text: text,
			}
			key := chunkKey(chunk.sourceKind, chunk.sourceID, chunk.index)
			desiredFingerprints[key] = chunk.fingerprint
			if old, ok := existing[key]; ok && old.fingerprint == chunk.fingerprint {
				report.Unchanged++
				continue
			}
			pending = append(pending, chunk)
			if len(pending) == defaultBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Delta{}, err
	}
	if err := flush(); err != nil {
		return Delta{}, err
	}
	if dimensions == 0 {
		vectors, err := i.Embedder.Embed(ctx, i.Model, []string{DocumentPrefix + "dimension probe"})
		if err != nil {
			return Delta{}, err
		}
		if len(vectors) != 1 || len(vectors[0]) == 0 {
			return Delta{}, fmt.Errorf("embedding model %s returned an empty dimension probe", i.Model)
		}
		dimensions = len(vectors[0])
		if err := ensureVectorTables(store, dimensions, i.Model); err != nil {
			return Delta{}, err
		}
	}
	if err := removeMissing(ctx, store, existing, desiredFingerprints, sourceKind, &report); err != nil {
		return Delta{}, err
	}
	report.Chunks = report.Added + report.Updated + report.Unchanged
	return report, nil
}

func validateSourceKind(sourceKind string, declared map[string]bool) error {
	if sourceKind == "" {
		return nil
	}
	if declared != nil {
		if declared[sourceKind] {
			return nil
		}
		return fmt.Errorf("unknown vector source %q", sourceKind)
	}
	switch sourceKind {
	case "memories", "exchanges", "thinking_blocks", "sessions":
		return nil
	default:
		return fmt.Errorf("unknown vector source %q", sourceKind)
	}
}

func (i Index) Query(ctx context.Context, text string, k int) ([]Result, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("semantic query is empty")
	}
	if k < 1 || k > 100 {
		return nil, fmt.Errorf("k must be between 1 and 100")
	}
	if _, err := os.Stat(i.VectorPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("vector search is not installed; run `roca vector install`")
	} else if err != nil {
		return nil, fmt.Errorf("inspect vector database: %w", err)
	}
	store, err := openSQLite(i.VectorPath, true)
	if err != nil {
		return nil, fmt.Errorf("open vector database: %w", err)
	}
	defer store.Close()
	_, model, dimensions, err := readIndexState(store)
	if err != nil {
		return nil, fmt.Errorf("read vector index: %w; run `roca vector install`", err)
	}
	if model == "" || dimensions == 0 {
		return nil, fmt.Errorf("vector index is not ready; run `roca vector install`")
	}
	vectors, err := i.Embedder.Embed(ctx, model, []string{QueryPrefix + text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) != dimensions {
		return nil, fmt.Errorf("query embedding has the wrong dimensions")
	}
	return i.queryVector(ctx, store, vectors[0], k)
}

func (i Index) queryVector(ctx context.Context, store *sql.DB, embedding []float32, k int) ([]Result, error) {
	if k < 1 || k > 100 {
		return nil, fmt.Errorf("k must be between 1 and 100")
	}
	candidates, err := nearest(ctx, store, vectorBlob(embedding), min(k*8, 800))
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, k)
	seen := map[string]bool{}
	misses := 0
	for _, candidate := range candidates {
		if seen[candidate.sourceID] {
			continue
		}
		seen[candidate.sourceID] = true
		body, err := i.Corpus.ResolveSource(ctx, candidate.kind, candidate.where)
		if err != nil {
			return nil, err
		}
		if body == "" {
			misses++
			if misses == maxUnresolvedCandidates {
				break
			}
			continue
		}
		sourceID := candidate.where.SourceID
		if sourceID == "" {
			sourceID = candidate.sourceID
		}
		results = append(results, Result{
			Rank: len(results) + 1, Score: 1 - candidate.distance,
			Database: i.Database, Table: candidate.kind, ID: sourceID,
			Source: candidate.kind, SourceID: candidate.sourceID, Text: body,
		})
		if len(results) == k {
			break
		}
	}
	return results, nil
}

func (i Index) waitingForIndex(path string) {
	if i.Notice != nil {
		i.Notice(fmt.Sprintf("another indexing run holds %s; waiting for it to finish", path))
	}
}

func (i Index) validate() error {
	if i.Corpus == nil || i.VectorPath == "" {
		return fmt.Errorf("corpus reader and vector database path are required")
	}
	if i.Model == "" {
		return fmt.Errorf("embedding model is required")
	}
	if i.Embedder == nil {
		return fmt.Errorf("embedding provider is required")
	}
	return nil
}

func chunks(text string, size, overlap int) []string {
	if text == "" || size <= 0 || overlap < 0 || overlap >= size {
		return nil
	}
	runes := []rune(text)
	var result []string
	for start := 0; start < len(runes); start += size - overlap {
		end := min(start+size, len(runes))
		result = append(result, string(runes[start:end]))
		if end == len(runes) {
			break
		}
	}
	return result
}

func (s sourceRow) stableID() string {
	escape := url.PathEscape
	if s.sourceID != "" {
		return s.kind + "/" + escape(s.sourceID)
	}
	switch s.kind {
	case "sessions":
		if s.sessionID != "" {
			return "sessions/" + escape(s.sessionID) + "/" + s.identity()
		}
	case "exchanges":
		if s.sessionID != "" && s.hasOrdinal {
			return fmt.Sprintf("exchanges/%s/%d/%s", escape(s.sessionID), s.ordinal, s.identity())
		}
		if s.sessionID != "" {
			return "exchanges/" + escape(s.sessionID) + "/unkeyed/" + s.identity()
		}
	case "thinking_blocks":
		if s.sessionID != "" && s.hasOrdinal && s.position != "" {
			return fmt.Sprintf("thinking_blocks/%s/%d/%s/%s", escape(s.sessionID), s.ordinal,
				escape(s.position), s.identity())
		}
		if s.sessionID != "" {
			return "thinking_blocks/" + escape(s.sessionID) + "/unkeyed/" + s.identity()
		}
	case "memories":
		switch {
		case s.cronSource != "" && s.filePath != "":
			return "memories/cron/" + escape(s.cronSource) + "/" + escape(s.filePath) + "/" + s.identity()
		case s.sessionID != "" && s.hasOrdinal:
			return fmt.Sprintf("memories/session/%s/%d/%s", escape(s.sessionID), s.ordinal, s.identity())
		default:
			return "memories/direct/" + s.identity()
		}
	}
	return s.kind + "/direct/" + s.identity()
}

func (s sourceRow) locator() locator {
	return locator{SourceID: s.sourceID, SessionID: s.sessionID, Ordinal: s.ordinal, HasOrdinal: s.hasOrdinal,
		Position: s.position, CronSource: s.cronSource, FilePath: s.filePath,
		Layer: s.layer, Origin: s.origin, CreatedAt: s.createdAt, Identity: s.identity()}
}

func (s sourceRow) identity() string {
	return fingerprint(strings.Join([]string{s.kind, s.layer, s.origin, s.createdAt, s.text}, "\x00"))
}

func ensureParent(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create vector data directory: %w", err)
	}
	return nil
}

func fingerprint(text string) string {
	return incrementality.ContentFingerprint(text)
}

func embeddingFingerprint(sourceKind, text string) string {
	if sourceKind == "sessions" {
		text = sessionEmbeddingTextVersion + "\x00" + text
	}
	return fingerprint(text)
}

func (s sourceRow) embeddingFingerprint(text string) string {
	version := s.fingerprintVersion
	if version == "" && s.kind == "sessions" {
		version = sessionEmbeddingTextVersion
	}
	if version != "" {
		return incrementality.ContentFingerprint(version, text)
	}
	return fingerprint(text)
}

func (s sourceRow) chunking() (int, int) {
	size, overlap := s.chunkSize, s.overlap
	if size <= 0 {
		size = defaultChunkSize
	}
	if overlap < 0 || overlap >= size || (s.chunkSize == 0 && overlap == 0) {
		overlap = defaultOverlap
	}
	return size, overlap
}

func chunkKey(kind, sourceID string, index int) string {
	return kind + "\x00" + sourceID + "\x00" + strconv.Itoa(index)
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	values := url.Values{"_pragma": {"busy_timeout(15000)"}}
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

func ensureBaseSchema(db *sql.DB) error {
	_, err := db.Exec(`PRAGMA journal_mode=WAL;
		DROP TABLE IF EXISTS census;
		DROP TABLE IF EXISTS census_totals;
		CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS chunks(
			id INTEGER PRIMARY KEY,
			source_kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			fingerprint TEXT NOT NULL,
			locator TEXT NOT NULL,
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			UNIQUE(source_kind, source_id, chunk_index)
		);
		INSERT OR IGNORE INTO meta(key,value) VALUES ('schema','` + vectorStorageSchema + `');`)
	if err != nil {
		return fmt.Errorf("initialize vector database: %w", err)
	}
	return nil
}

func readIndexState(db *sql.DB) (map[string]storedChunk, string, int, error) {
	state := map[string]storedChunk{}
	rows, err := db.Query(`SELECT id, source_kind, source_id, chunk_index, fingerprint FROM chunks`)
	if err != nil {
		return nil, "", 0, err
	}
	for rows.Next() {
		var item storedChunk
		var kind, sourceID string
		var index int
		if err := rows.Scan(&item.id, &kind, &sourceID, &index, &item.fingerprint); err != nil {
			rows.Close()
			return nil, "", 0, err
		}
		item.sourceKind = kind
		state[chunkKey(kind, sourceID, index)] = item
	}
	if err := rows.Close(); err != nil {
		return nil, "", 0, err
	}
	var model string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='model'`).Scan(&model)
	var dimensionText string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='dimensions'`).Scan(&dimensionText)
	dimensions, _ := strconv.Atoi(dimensionText)
	return state, model, dimensions, nil
}

func resetIndex(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS ann_embeddings;
		DROP TABLE IF EXISTS embeddings;
		DELETE FROM chunks;
		DELETE FROM meta WHERE key NOT IN ('schema','owner');
		INSERT OR IGNORE INTO meta(key,value) VALUES ('schema','` + vectorStorageSchema + `');`)
	if err != nil {
		return fmt.Errorf("reset vector index for the selected model: %w", err)
	}
	return nil
}

func ensureVectorTables(db *sql.DB, dimensions int, model string) error {
	if dimensions < 8 || dimensions > 65536 || dimensions%8 != 0 {
		return fmt.Errorf("embedding dimension %d is outside the supported range", dimensions)
	}
	statements := []string{
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS embeddings USING vec0(embedding float[%d] distance_metric=cosine)`, dimensions),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS ann_embeddings USING vec0(embedding bit[%d])`, dimensions),
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("create sqlite-vec index: %w", err)
		}
	}
	_, err := db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES ('model',?),('dimensions',?)`, model, strconv.Itoa(dimensions))
	return err
}

func writeBatch(ctx context.Context, db *sql.DB, chunks []desiredChunk, vectors [][]float32,
	existing map[string]storedChunk, report *Delta) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for n, chunk := range chunks {
		key := chunkKey(chunk.sourceKind, chunk.sourceID, chunk.index)
		where, err := json.Marshal(chunk.locator)
		if err != nil {
			return err
		}
		if old, ok := existing[key]; ok {
			if err := deleteEmbeddings(ctx, tx, old.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE chunks SET fingerprint=?,locator=?,updated_at=datetime('now') WHERE id=?`, chunk.fingerprint, string(where), old.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings(rowid,embedding) VALUES (?,?)`, old.id, vectorBlob(vectors[n])); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ann_embeddings(rowid,embedding) VALUES (?,vec_quantize_binary(?))`, old.id, vectorBlob(vectors[n])); err != nil {
				return err
			}
			report.Updated++
			existing[key] = storedChunk{id: old.id, sourceKind: chunk.sourceKind, fingerprint: chunk.fingerprint}
			continue
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO chunks(source_kind,source_id,chunk_index,fingerprint,locator) VALUES (?,?,?,?,?)`,
			chunk.sourceKind, chunk.sourceID, chunk.index, chunk.fingerprint, string(where))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings(rowid,embedding) VALUES (?,?)`, id, vectorBlob(vectors[n])); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ann_embeddings(rowid,embedding) VALUES (?,vec_quantize_binary(?))`, id, vectorBlob(vectors[n])); err != nil {
			return err
		}
		report.Added++
		existing[key] = storedChunk{id: id, sourceKind: chunk.sourceKind, fingerprint: chunk.fingerprint}
	}
	return tx.Commit()
}

func removeMissing(ctx context.Context, db *sql.DB, existing map[string]storedChunk,
	desiredFingerprints map[string]string,
	sourceKind string, report *Delta) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, old := range existing {
		if sourceKind != "" && old.sourceKind != sourceKind {
			continue
		}
		if _, desired := desiredFingerprints[key]; desired {
			continue
		}
		if err := deleteEmbeddings(ctx, tx, old.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id=?`, old.id); err != nil {
			return err
		}
		report.Removed++
	}
	return tx.Commit()
}

func deleteEmbeddings(ctx context.Context, tx *sql.Tx, rowID int64) error {
	for _, table := range []string{"ann_embeddings", "embeddings"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid=?`, rowID); err != nil {
			return err
		}
	}
	return nil
}

type neighbor struct {
	kind     string
	sourceID string
	distance float64
	where    locator
}

func nearest(ctx context.Context, db *sql.DB, vector []byte, k int) ([]neighbor, error) {
	rows, err := db.QueryContext(ctx, `WITH candidates AS (
			SELECT rowid FROM ann_embeddings
			WHERE embedding MATCH vec_quantize_binary(?) AND k = ?
		)
		SELECT c.source_kind,c.source_id,c.locator,vec_distance_cosine(e.embedding,?) AS distance
		FROM candidates a
		JOIN embeddings e ON e.rowid=a.rowid
		JOIN chunks c ON c.id=a.rowid
		ORDER BY distance`, vector, k, vector)
	if err != nil {
		return nil, fmt.Errorf("search sqlite-vec index: %w", err)
	}
	defer rows.Close()
	var result []neighbor
	for rows.Next() {
		var item neighbor
		var raw string
		if err := rows.Scan(&item.kind, &item.sourceID, &raw, &item.distance); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &item.where); err != nil {
			return nil, fmt.Errorf("decode source locator: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func vectorBlob(vector []float32) []byte {
	result := make([]byte, len(vector)*4)
	for n, value := range vector {
		binary.LittleEndian.PutUint32(result[n*4:], math.Float32bits(value))
	}
	return result
}

type CompactReport struct {
	PagesBefore    int64 `json:"pages_before"`
	PagesAfter     int64 `json:"pages_after"`
	BytesBefore    int64 `json:"bytes_before"`
	BytesAfter     int64 `json:"bytes_after"`
	BytesReclaimed int64 `json:"bytes_reclaimed"`
	LiveChunks     int64 `json:"live_chunks"`
}

type compactSnapshot struct {
	chunks int64
	pages  int64
	kinds  map[string]int64
}

func Compact(ctx context.Context, vectorPath string) (CompactReport, error) {
	if vectorPath == "" {
		return CompactReport{}, fmt.Errorf("vector database path is required")
	}
	if err := ctx.Err(); err != nil {
		return CompactReport{}, err
	}
	release, busy, err := tryLockIndex(vectorPath + ".index.lock")
	if err != nil {
		return CompactReport{}, fmt.Errorf("lock vector index: %w", err)
	}
	if busy {
		return CompactReport{}, fmt.Errorf("vector index is busy; another ingest holds %s", vectorPath+".index.lock")
	}
	defer release()

	existing, err := os.OpenFile(vectorPath, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return CompactReport{}, fmt.Errorf("vector search is not initialized; run `roca vector install`")
		}
		return CompactReport{}, fmt.Errorf("inspect vector database: %w", err)
	}
	if err := existing.Close(); err != nil {
		return CompactReport{}, fmt.Errorf("inspect vector database: %w", err)
	}

	source, err := openSQLite(vectorPath, false)
	if err != nil {
		return CompactReport{}, fmt.Errorf("open vector database: %w", err)
	}
	sourceOpen := true
	defer func() {
		if sourceOpen {
			_ = source.Close()
		}
	}()
	if err := ensureBaseSchema(source); err != nil {
		return CompactReport{}, err
	}
	info, err := checkpointedSourceInfo(source, vectorPath)
	if err != nil {
		return CompactReport{}, err
	}
	before, model, dimensions, err := inspectCompactStore(ctx, source)
	if err != nil {
		return CompactReport{}, fmt.Errorf("inspect vector index: %w", err)
	}
	if model == "" || dimensions == 0 {
		return CompactReport{}, fmt.Errorf("vector index is not ready; run `roca vector install`")
	}

	temporaryFile, err := os.CreateTemp(filepath.Dir(vectorPath), "."+filepath.Base(vectorPath)+".compact-*")
	if err != nil {
		return CompactReport{}, fmt.Errorf("create compacted vector database: %w", err)
	}
	temporary := temporaryFile.Name()
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporary)
		return CompactReport{}, err
	}
	defer removeCompactFiles(temporary)
	if err := os.Chmod(temporary, info.Mode().Perm()); err != nil {
		return CompactReport{}, fmt.Errorf("secure compacted vector database: %w", err)
	}

	target, err := openSQLite(temporary, false)
	if err != nil {
		return CompactReport{}, fmt.Errorf("open compacted vector database: %w", err)
	}
	targetOpen := true
	defer func() {
		if targetOpen {
			_ = target.Close()
		}
	}()
	if err := buildCompactedStore(ctx, target, vectorPath, model, dimensions); err != nil {
		return CompactReport{}, err
	}
	after, _, _, err := inspectCompactStore(ctx, target)
	if err != nil {
		return CompactReport{}, fmt.Errorf("verify compacted vector index: %w", err)
	}
	if err := verifyCompaction(before, after); err != nil {
		return CompactReport{}, err
	}
	if err := sqliteIntegrityCheck(ctx, target); err != nil {
		return CompactReport{}, fmt.Errorf("verify compacted vector database: %w", err)
	}
	if err := finishReplacementDatabase(target); err != nil {
		return CompactReport{}, err
	}
	if err := target.Close(); err != nil {
		return CompactReport{}, fmt.Errorf("close compacted vector database: %w", err)
	}
	targetOpen = false
	if err := source.Close(); err != nil {
		return CompactReport{}, fmt.Errorf("close vector database: %w", err)
	}
	sourceOpen = false

	afterInfo, err := os.Stat(temporary)
	if err != nil {
		return CompactReport{}, fmt.Errorf("measure compacted vector database: %w", err)
	}
	if err := syncFile(temporary); err != nil {
		return CompactReport{}, fmt.Errorf("sync compacted vector database: %w", err)
	}
	if err := replaceFile(temporary, vectorPath); err != nil {
		return CompactReport{}, fmt.Errorf("replace vector database: %w", err)
	}
	reclaimed := info.Size() - afterInfo.Size()
	if reclaimed < 0 {
		reclaimed = 0
	}
	return CompactReport{
		PagesBefore: before.pages, PagesAfter: after.pages,
		BytesBefore: info.Size(), BytesAfter: afterInfo.Size(),
		BytesReclaimed: reclaimed, LiveChunks: after.chunks,
	}, nil
}

func checkpointedSourceInfo(db *sql.DB, path string) (os.FileInfo, error) {
	if err := checkpointForReplacement(db); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect vector database: %w", err)
	}
	return info, nil
}

func checkpointForReplacement(db *sql.DB) error {
	var busy, logFrames, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint vector database: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint vector database: active readers prevented a safe replacement")
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return fmt.Errorf("prepare vector database replacement: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("prepare vector database replacement: journal mode remained %s", mode)
	}
	return nil
}

func buildCompactedStore(ctx context.Context, target *sql.DB, sourcePath, model string, dimensions int) error {
	if err := ensureBaseSchema(target); err != nil {
		return err
	}
	if err := ensureVectorTables(target, dimensions, model); err != nil {
		return err
	}
	if _, err := target.ExecContext(ctx, `PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF`); err != nil {
		return fmt.Errorf("prepare compacted vector database: %w", err)
	}
	if _, err := target.ExecContext(ctx, `ATTACH DATABASE ? AS source`, sourcePath); err != nil {
		return fmt.Errorf("attach vector database for compaction: %w", err)
	}
	attached := true
	defer func() {
		if attached {
			_, _ = target.Exec(`DETACH DATABASE source`)
		}
	}()
	statements := []struct {
		name string
		sql  string
	}{
		{"metadata", `INSERT OR REPLACE INTO main.meta(key,value) SELECT key,value FROM source.meta`},
		{"chunk identities", `INSERT INTO main.chunks(id,source_kind,source_id,chunk_index,fingerprint,locator,updated_at)
			SELECT id,source_kind,source_id,chunk_index,fingerprint,locator,updated_at FROM source.chunks ORDER BY id`},
		{"float embeddings", `INSERT INTO main.embeddings(rowid,embedding)
			SELECT rowid,vec_f32(embedding) FROM source.embeddings`},
		{"ANN embeddings", `INSERT INTO main.ann_embeddings(rowid,embedding)
			SELECT rowid,vec_quantize_binary(embedding) FROM source.embeddings`},
	}
	for _, statement := range statements {
		if _, err := target.ExecContext(ctx, statement.sql); err != nil {
			return fmt.Errorf("copy compacted vector database %s: %w", statement.name, err)
		}
	}
	if _, err := target.ExecContext(ctx, `DETACH DATABASE source`); err != nil {
		return fmt.Errorf("detach vector database after compaction: %w", err)
	}
	attached = false
	return nil
}

func inspectCompactStore(ctx context.Context, db *sql.DB) (compactSnapshot, string, int, error) {
	snapshot := compactSnapshot{kinds: map[string]int64{}}
	var model, dimensionsText string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='model'`).Scan(&model); err != nil {
		return snapshot, "", 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='dimensions'`).Scan(&dimensionsText); err != nil {
		return snapshot, "", 0, err
	}
	dimensions, err := strconv.Atoi(dimensionsText)
	if err != nil {
		return snapshot, "", 0, fmt.Errorf("invalid embedding dimensions %q", dimensionsText)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks`).Scan(&snapshot.chunks); err != nil {
		return snapshot, "", 0, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings_vector_chunks00`).Scan(&snapshot.pages); err != nil {
		return snapshot, "", 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT source_kind,COUNT(*) FROM chunks GROUP BY source_kind`)
	if err != nil {
		return snapshot, "", 0, err
	}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			rows.Close()
			return snapshot, "", 0, err
		}
		snapshot.kinds[kind] = count
	}
	if err := rows.Close(); err != nil {
		return snapshot, "", 0, err
	}
	for _, table := range []string{"embeddings", "ann_embeddings"} {
		var missing, orphaned int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT id FROM chunks EXCEPT SELECT rowid FROM `+table+`)`).Scan(&missing); err != nil {
			return snapshot, "", 0, err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT rowid FROM `+table+` EXCEPT SELECT id FROM chunks)`).Scan(&orphaned); err != nil {
			return snapshot, "", 0, err
		}
		if missing != 0 || orphaned != 0 {
			return snapshot, "", 0, fmt.Errorf("%s has %d missing and %d orphaned vectors", table, missing, orphaned)
		}
	}
	return snapshot, model, dimensions, nil
}

func verifyCompaction(before, after compactSnapshot) error {
	if before.chunks != after.chunks {
		return fmt.Errorf("compacted vector index changed live chunk count from %d to %d", before.chunks, after.chunks)
	}
	if len(before.kinds) != len(after.kinds) {
		return fmt.Errorf("compacted vector index changed source kind counts")
	}
	for kind, count := range before.kinds {
		if after.kinds[kind] != count {
			return fmt.Errorf("compacted vector index changed %s count from %d to %d", kind, count, after.kinds[kind])
		}
	}
	return nil
}

func sqliteIntegrityCheck(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

func finishReplacementDatabase(db *sql.DB) error {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		return fmt.Errorf("finish compacted vector database: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("finish compacted vector database: journal mode remained %s", mode)
	}
	if _, err := db.Exec(`PRAGMA synchronous=FULL`); err != nil {
		return fmt.Errorf("finish compacted vector database: %w", err)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func removeCompactFiles(path string) {
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
}
