// Package vector owns the optional local semantic-search index.
package vector

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
	_ "modernc.org/sqlite/vec"
)

const (
	defaultChunkSize                 = 4000
	defaultOverlap                   = 400
	defaultBatchSize                 = 64
	walkPageSize                     = 500
	deprecatedLayerReconciliationKey = "deprecated_layers_reconciled_v1"
	// maxUnresolvedCandidates bounds a query against an index the corpus has moved
	// under. Each resolution is one `roca exec` process, so a wholly stale index
	// would otherwise spend one process per candidate to answer nothing.
	maxUnresolvedCandidates = 32
)

type Index struct {
	Corpus     Corpus
	VectorPath string
	Model      string
	Embedder   Embedder
	Notice     func(string)
	ReadOnly   bool
}

type Corpus interface {
	WalkSources(context.Context, func(sourceRow) error) error
	ResolveSource(context.Context, string, Locator) (string, error)
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
	Source   string  `json:"source"`
	SourceID string  `json:"source_id"`
	Locator  Locator `json:"locator"`
	Text     string  `json:"text"`
}

type sourceRow struct {
	kind       string
	text       string
	sessionID  string
	ordinal    int64
	hasOrdinal bool
	position   string
	cronSource string
	filePath   string
	layer      string
	origin     string
	createdAt  string
}

type Locator struct {
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
	locator     Locator
	text        string
}

type storedChunk struct {
	id          int64
	fingerprint string
	locator     Locator
}

type locatorUpdate struct {
	id      int64
	locator Locator
}

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
	if err := i.validate(); err != nil {
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
	existing, model, dimensions, _, err := readIndexState(store)
	if err != nil {
		return Delta{}, err
	}
	if model != "" && model != i.Model {
		if err := resetIndex(store); err != nil {
			return Delta{}, err
		}
		existing, dimensions = map[string]storedChunk{}, 0
	}
	if dimensions > 0 {
		if err := ensureVectorTables(store, dimensions, i.Model); err != nil {
			return Delta{}, err
		}
	}

	report := Delta{}
	initial := make(map[string]storedChunk, len(existing))
	for key, chunk := range existing {
		initial[key] = chunk
	}
	seen := make(map[string]bool, len(existing))
	pending := make([]desiredChunk, 0, defaultBatchSize)
	locatorUpdates := make(map[int64]locatorUpdate)
	if _, err := store.ExecContext(ctx, `PRAGMA temp_store=FILE;
		CREATE TEMP TABLE vector_desired_sources(
			source_kind TEXT NOT NULL,
			source_id TEXT NOT NULL,
			source_order INTEGER NOT NULL,
			text TEXT NOT NULL,
			locator TEXT NOT NULL,
			PRIMARY KEY(source_kind, source_id)
		);
		CREATE INDEX temp.vector_desired_sources_order ON vector_desired_sources(source_order)`); err != nil {
		return Delta{}, fmt.Errorf("initialize vector reconciliation: %w", err)
	}
	var nextSourceOrder int64
	var sourceOrder int64 = -1
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
		if err := writeBatch(ctx, store, pending, vectors, existing, locatorUpdates); err != nil {
			return err
		}
		pending = pending[:0]
		return nil
	}
	drainSources := func() error {
		for {
			var kind, sourceID, sourceText, rawLocator string
			var order int64
			err := store.QueryRowContext(ctx, `
				SELECT source_kind,source_id,source_order,text,locator
				FROM temp.vector_desired_sources
				WHERE source_order > ?
				ORDER BY source_order
				LIMIT 1
			`, sourceOrder).Scan(&kind, &sourceID, &order, &sourceText, &rawLocator)
			if err == sql.ErrNoRows {
				break
			}
			if err != nil {
				return fmt.Errorf("read vector reconciliation: %w", err)
			}
			sourceOrder = order
			var where Locator
			if err := json.Unmarshal([]byte(rawLocator), &where); err != nil {
				return fmt.Errorf("decode %s source locator: %w", kind, err)
			}
			sourceChunks := chunks(sourceText, defaultChunkSize, defaultOverlap)
			desiredKeys := make(map[string]struct{}, len(sourceChunks))
			for chunkIndex := range sourceChunks {
				desiredKeys[chunkKey(kind, sourceID, chunkIndex)] = struct{}{}
			}
			if err := removeSourceMissing(ctx, store, existing, kind, sourceID, desiredKeys); err != nil {
				return err
			}
			for chunkIndex, text := range sourceChunks {
				chunk := desiredChunk{
					sourceKind: kind, sourceID: sourceID, index: chunkIndex,
					fingerprint: fingerprint(text), locator: where, text: text,
				}
				key := chunkKey(chunk.sourceKind, chunk.sourceID, chunk.index)
				seen[key] = true
				if old, ok := existing[key]; ok && old.fingerprint == chunk.fingerprint {
					if old.locator != chunk.locator {
						locatorUpdates[old.id] = locatorUpdate{id: old.id, locator: chunk.locator}
						existing[key] = storedChunk{id: old.id, fingerprint: old.fingerprint, locator: chunk.locator}
					}
					continue
				}
				pending = append(pending, chunk)
				if len(pending) == defaultBatchSize {
					if err := flush(); err != nil {
						return err
					}
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}
		if sourceOrder >= 0 {
			if _, err := store.ExecContext(ctx,
				`DELETE FROM temp.vector_desired_sources WHERE source_order <= ?`, sourceOrder); err != nil {
				return fmt.Errorf("clear vector reconciliation: %w", err)
			}
		}
		return nil
	}

	stagedSources := 0
	err = i.Corpus.WalkSources(ctx, func(source sourceRow) error {
		if !source.indexable() {
			return nil
		}
		report.Sources++
		sourceID := source.stableID()
		where, err := json.Marshal(source.locator())
		if err != nil {
			return fmt.Errorf("encode %s source locator: %w", source.kind, err)
		}
		_, err = store.ExecContext(ctx, `
			INSERT INTO temp.vector_desired_sources(source_kind,source_id,source_order,text,locator)
			VALUES (?,?,?,?,?)
			ON CONFLICT(source_kind,source_id) DO UPDATE SET text=excluded.text,locator=excluded.locator
		`, source.kind, sourceID, nextSourceOrder, source.text, string(where))
		nextSourceOrder++
		if err != nil {
			return err
		}
		stagedSources++
		if stagedSources >= defaultBatchSize {
			if err := drainSources(); err != nil {
				return err
			}
			stagedSources = 0
		}
		return nil
	})
	if err != nil {
		return Delta{}, err
	}
	if err := drainSources(); err != nil {
		return Delta{}, err
	}
	if err := updateLocators(ctx, store, locatorUpdates); err != nil {
		return Delta{}, err
	}
	if err := removeMissing(ctx, store, existing, seen); err != nil {
		return Delta{}, err
	}
	if err := markDeprecatedLayersReconciled(ctx, store); err != nil {
		return Delta{}, err
	}
	delta := deltaBetween(initial, existing)
	delta.Sources = report.Sources
	return delta, nil
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
	_, model, dimensions, deprecated, err := readIndexState(store)
	if err != nil {
		return nil, fmt.Errorf("read vector index: %w; run `roca vector install`", err)
	}
	if model == "" || dimensions == 0 {
		return nil, fmt.Errorf("vector index is not ready; run `roca vector install`")
	}
	reconciled, err := deprecatedLayersReconciled(ctx, store)
	if err != nil {
		return nil, fmt.Errorf("inspect vector reconciliation: %w", err)
	}
	if deprecated && i.ReadOnly {
		return nil, fmt.Errorf("vector index contains retired chunks; run `roca vector ingest --delta` with writes enabled")
	}
	if !i.ReadOnly && (!reconciled || deprecated) {
		store.Close()
		var reconcileErr error
		if deprecated {
			reconcileErr = i.purgeDeprecatedChunks(ctx)
		} else {
			reconcileErr = i.markDeprecatedLayersReconciled(ctx)
		}
		if reconcileErr != nil {
			return nil, fmt.Errorf("reconcile deprecated vector chunks: %w", reconcileErr)
		}
		store, err = openSQLite(i.VectorPath, true)
		if err != nil {
			return nil, fmt.Errorf("reopen vector database: %w", err)
		}
		defer store.Close()
		_, model, dimensions, _, err = readIndexState(store)
		if err != nil {
			return nil, fmt.Errorf("read vector index after reconciliation: %w; run `roca vector install`", err)
		}
	}
	vectors, err := i.Embedder.Embed(ctx, model, []string{QueryPrefix + text})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 || len(vectors[0]) != dimensions {
		return nil, fmt.Errorf("query embedding has the wrong dimensions")
	}
	candidates, err := nearest(ctx, store, vectorBlob(vectors[0]), min(k*8, 800))
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
		results = append(results, Result{
			Rank: len(results) + 1, Score: 1 - candidate.distance,
			Source: candidate.kind, SourceID: candidate.sourceID, Locator: candidate.where, Text: body,
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

func (i Index) purgeDeprecatedChunks(ctx context.Context) error {
	release, err := lockIndex(ctx, i.VectorPath+".index.lock", i.waitingForIndex)
	if err != nil {
		return err
	}
	defer release()

	store, err := openSQLite(i.VectorPath, false)
	if err != nil {
		return err
	}
	defer store.Close()
	return purgeDeprecatedChunks(ctx, store)
}

func (i Index) markDeprecatedLayersReconciled(ctx context.Context) error {
	release, err := lockIndex(ctx, i.VectorPath+".index.lock", i.waitingForIndex)
	if err != nil {
		return err
	}
	defer release()

	store, err := openSQLite(i.VectorPath, false)
	if err != nil {
		return err
	}
	defer store.Close()
	return markDeprecatedLayersReconciled(ctx, store)
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
	switch s.kind {
	case "sessions":
		if s.sessionID != "" {
			return "sessions/" + escape(s.sessionID)
		}
	case "exchanges":
		if s.sessionID != "" && s.hasOrdinal {
			return fmt.Sprintf("exchanges/%s/%d", escape(s.sessionID), s.ordinal)
		}
		if s.sessionID != "" {
			return "exchanges/" + escape(s.sessionID) + "/unkeyed/" + s.identity()
		}
	case "thinking_blocks":
		if s.sessionID != "" && s.hasOrdinal && s.position != "" {
			return fmt.Sprintf("thinking_blocks/%s/%d/%s", escape(s.sessionID), s.ordinal, escape(s.position))
		}
		if s.sessionID != "" {
			return "thinking_blocks/" + escape(s.sessionID) + "/unkeyed/" + s.identity()
		}
	case "memories":
		switch {
		case s.cronSource != "" && s.filePath != "":
			return "memories/cron/" + escape(s.cronSource) + "/" + escape(s.filePath)
		case s.sessionID != "" && s.hasOrdinal:
			return fmt.Sprintf("memories/session/%s/%d", escape(s.sessionID), s.ordinal)
		default:
			return "memories/direct/" + s.identity()
		}
	}
	return s.kind + "/direct/" + s.identity()
}

func (s sourceRow) locator() Locator {
	return Locator{SessionID: s.sessionID, Ordinal: s.ordinal, HasOrdinal: s.hasOrdinal,
		Position: s.position, CronSource: s.cronSource, FilePath: s.filePath,
		Layer: s.layer, Origin: s.origin, CreatedAt: s.createdAt, Identity: s.identity()}
}

func (s sourceRow) indexable() bool {
	return s.kind != "memories" || !deprecatedMemoryLayer(s.layer)
}

func deprecatedMemoryLayer(layer string) bool {
	return strings.HasPrefix(strings.ToLower(layer), "rocodata_")
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
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

func chunkKey(kind, sourceID string, index int) string {
	return sourceKey(kind, sourceID) + "\x00" + strconv.Itoa(index)
}

func sourceKey(kind, sourceID string) string {
	return kind + "\x00" + sourceID
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
		);`)
	if err != nil {
		return fmt.Errorf("initialize vector database: %w", err)
	}
	return nil
}

func readIndexState(db *sql.DB) (map[string]storedChunk, string, int, bool, error) {
	state := map[string]storedChunk{}
	rows, err := db.Query(`SELECT id, source_kind, source_id, chunk_index, fingerprint, locator FROM chunks`)
	if err != nil {
		return nil, "", 0, false, err
	}
	deprecated := false
	for rows.Next() {
		var item storedChunk
		var kind, sourceID string
		var index int
		var rawLocator string
		if err := rows.Scan(&item.id, &kind, &sourceID, &index, &item.fingerprint, &rawLocator); err != nil {
			rows.Close()
			return nil, "", 0, false, err
		}
		if json.Unmarshal([]byte(rawLocator), &item.locator) == nil && kind == "memories" && deprecatedMemoryLayer(item.locator.Layer) {
			deprecated = true
		}
		state[chunkKey(kind, sourceID, index)] = item
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", 0, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, "", 0, false, err
	}
	var model string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='model'`).Scan(&model)
	var dimensionText string
	_ = db.QueryRow(`SELECT value FROM meta WHERE key='dimensions'`).Scan(&dimensionText)
	dimensions, _ := strconv.Atoi(dimensionText)
	return state, model, dimensions, deprecated, nil
}

func resetIndex(db *sql.DB) error {
	_, err := db.Exec(`DROP TABLE IF EXISTS ann_embeddings; DROP TABLE IF EXISTS embeddings; DELETE FROM chunks; DELETE FROM meta;`)
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
	existing map[string]storedChunk, locatorUpdates map[int64]locatorUpdate) error {
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
			locatorUpdates[old.id] = locatorUpdate{id: old.id, locator: chunk.locator}
			existing[key] = storedChunk{id: old.id, fingerprint: chunk.fingerprint, locator: chunk.locator}
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
		existing[key] = storedChunk{id: id, fingerprint: chunk.fingerprint, locator: chunk.locator}
	}
	return tx.Commit()
}

func updateLocators(ctx context.Context, db *sql.DB, updates map[int64]locatorUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, update := range updates {
		where, err := json.Marshal(update.locator)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chunks SET locator=?,updated_at=datetime('now') WHERE id=?`, string(where), update.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func removeMissing(ctx context.Context, db *sql.DB, existing map[string]storedChunk, seen map[string]bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var removed []string
	for key, old := range existing {
		if seen[key] {
			continue
		}
		if err := deleteEmbeddings(ctx, tx, old.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id=?`, old.id); err != nil {
			return err
		}
		removed = append(removed, key)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, key := range removed {
		delete(existing, key)
	}
	return nil
}

func removeSourceMissing(ctx context.Context, db *sql.DB, existing map[string]storedChunk,
	kind, sourceID string, desired map[string]struct{}) error {
	prefix := sourceKey(kind, sourceID) + "\x00"
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var removed []string
	for key, old := range existing {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, ok := desired[key]; ok {
			continue
		}
		if err := deleteEmbeddings(ctx, tx, old.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id=?`, old.id); err != nil {
			return err
		}
		removed = append(removed, key)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, key := range removed {
		delete(existing, key)
	}
	return nil
}

func deltaBetween(initial, existing map[string]storedChunk) Delta {
	var delta Delta
	for key, current := range existing {
		previous, ok := initial[key]
		if !ok {
			delta.Added++
			continue
		}
		if previous.fingerprint != current.fingerprint || previous.locator != current.locator {
			delta.Updated++
			continue
		}
		delta.Unchanged++
	}
	for key := range initial {
		if _, ok := existing[key]; !ok {
			delta.Removed++
		}
	}
	delta.Chunks = delta.Added + delta.Updated + delta.Unchanged
	return delta
}

func deleteEmbeddings(ctx context.Context, tx *sql.Tx, rowID int64) error {
	for _, table := range []string{"ann_embeddings", "embeddings"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE rowid=?`, rowID); err != nil {
			return err
		}
	}
	return nil
}

func purgeDeprecatedChunks(ctx context.Context, db *sql.DB) error {
	var chunksTable int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='chunks')`).Scan(&chunksTable); err != nil {
		return err
	}
	if chunksTable == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id FROM chunks WHERE source_kind='memories' AND lower(COALESCE(json_extract(locator,'$.layer'),'')) LIKE 'rocodata\_%' ESCAPE '\'`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return markDeprecatedLayersReconciled(ctx, db)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if err := deleteEmbeddings(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id=?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES (?,?)`, deprecatedLayerReconciliationKey, "1"); err != nil {
		return err
	}
	return tx.Commit()
}

func deprecatedLayersReconciled(ctx context.Context, db *sql.DB) (bool, error) {
	var value string
	err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, deprecatedLayerReconciliationKey).Scan(&value)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return value == "1", nil
}

func markDeprecatedLayersReconciled(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES (?,?)`, deprecatedLayerReconciliationKey, "1")
	return err
}

type neighbor struct {
	kind     string
	sourceID string
	distance float64
	where    Locator
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
		WHERE c.source_kind <> 'memories' OR lower(COALESCE(json_extract(c.locator,'$.layer'),'')) NOT LIKE 'rocodata\_%' ESCAPE '\'
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
