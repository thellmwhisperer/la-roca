package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const legacySeedBatchSize = 64

type legacySeed struct {
	legacyID int64
	targetID int64
	chunk    desiredChunk
}

func (f Federation) seedSidecarsFromLegacyMonolith(ctx context.Context, legacyPath string) error {
	release, err := lockIndex(ctx, legacyPath+".index.lock", func(path string) {
		if f.Notice != nil {
			f.Notice("another indexing run holds " + path + "; waiting before legacy vector reuse")
		}
	})
	if err != nil {
		return fmt.Errorf("lock legacy vector index: %w", err)
	}
	defer release()

	legacy, err := openSQLite(legacyPath, true)
	if err != nil {
		return fmt.Errorf("open legacy vector index: %w", err)
	}
	defer legacy.Close()

	_, model, dimensions, err := inspectCompactStore(ctx, legacy)
	if err != nil {
		return fmt.Errorf("inspect legacy vector index: %w", err)
	}
	if model != f.Model {
		return fmt.Errorf("legacy vector index uses model %s, want %s", model, f.Model)
	}
	lookup, err := legacyChunkLookup(ctx, legacy)
	if err != nil {
		return err
	}

	for _, database := range f.databases {
		if database.Plugin != "roca-corpus" && database.Plugin != "roca-ops" {
			continue
		}
		seeded, err := f.seedLegacySidecar(ctx, legacy, lookup, database, dimensions)
		if err != nil {
			f.notifyLegacySeed(database.owner(), err)
			continue
		}
		if seeded > 0 && f.Notice != nil {
			f.Notice(fmt.Sprintf("reused %d embeddings from the legacy vector index for %s", seeded, database.owner()))
		}
	}
	return nil
}

func legacyChunkLookup(ctx context.Context, legacy *sql.DB) (map[string]int64, error) {
	rows, err := legacy.QueryContext(ctx, `SELECT id,source_kind,chunk_index,fingerprint FROM chunks ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read legacy vector chunks: %w", err)
	}
	defer rows.Close()
	lookup := map[string]int64{}
	for rows.Next() {
		var id int64
		var kind, fingerprint string
		var index int
		if err := rows.Scan(&id, &kind, &index, &fingerprint); err != nil {
			return nil, fmt.Errorf("read legacy vector chunk: %w", err)
		}
		key := chunkKey(kind, fingerprint, "", index)
		if lookup[key] == 0 {
			lookup[key] = id
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read legacy vector chunks: %w", err)
	}
	return lookup, nil
}

func (f Federation) seedLegacySidecar(ctx context.Context, legacy *sql.DB,
	lookup map[string]int64, database vectorDatabase, dimensions int) (int, error) {
	path := SidecarPath(f.databasePath(database))
	if err := ensureParent(path); err != nil {
		return 0, err
	}
	release, err := lockIndex(ctx, path+".index.lock", func(waiting string) {
		if f.Notice != nil {
			f.Notice("another indexing run holds " + waiting + "; waiting before legacy vector reuse")
		}
	})
	if err != nil {
		return 0, fmt.Errorf("lock legacy sidecar seed for %s: %w", database.owner(), err)
	}
	defer release()
	existingSidecar := false
	if _, err := os.Stat(path); err == nil {
		existingSidecar = true
		if err := assertSidecarOwner(path, database.owner()); err != nil {
			return 0, err
		}
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("inspect sidecar for %s: %w", database.owner(), err)
	}
	targetPath := path
	temporary := ""
	if !existingSidecar {
		temporaryFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".legacy-*")
		if err != nil {
			return 0, fmt.Errorf("create legacy sidecar seed: %w", err)
		}
		temporary = temporaryFile.Name()
		if err := temporaryFile.Close(); err != nil {
			_ = os.Remove(temporary)
			return 0, err
		}
		defer removeCompactFiles(temporary)
		if err := os.Chmod(temporary, 0o600); err != nil {
			return 0, fmt.Errorf("secure legacy sidecar seed: %w", err)
		}
		targetPath = temporary
	}

	target, err := openSQLite(targetPath, false)
	if err != nil {
		return 0, fmt.Errorf("open legacy sidecar seed: %w", err)
	}
	open := true
	defer func() {
		if open {
			_ = target.Close()
		}
	}()
	if err := ensureBaseSchema(target); err != nil {
		return 0, err
	}
	existing, existingModel, existingDimensions, err := readIndexState(target)
	if err != nil {
		return 0, fmt.Errorf("read sidecar seed state for %s: %w", database.owner(), err)
	}
	if existingModel != "" && existingModel != f.Model {
		return 0, fmt.Errorf("existing sidecar for %s uses model %s, want %s",
			database.owner(), existingModel, f.Model)
	}
	if existingDimensions != 0 && existingDimensions != dimensions {
		return 0, fmt.Errorf("existing sidecar for %s has %d dimensions, want %d",
			database.owner(), existingDimensions, dimensions)
	}
	if err := ensureVectorTables(target, dimensions, f.Model); err != nil {
		return 0, err
	}

	reader := DeclaredCorpus{Core: f.Core, Database: database}
	seeds := make([]legacySeed, 0, legacySeedBatchSize)
	seeded := 0
	flush := func() error {
		if len(seeds) == 0 {
			return nil
		}
		count, err := copyLegacySeedBatch(ctx, legacy, target, seeds, dimensions)
		if err != nil {
			return err
		}
		seeded += count
		seeds = seeds[:0]
		return nil
	}
	err = reader.WalkSources(ctx, "", func(source sourceRow) error {
		header := source.header()
		for index, text := range source.window() {
			input := header + text
			if input != text {
				continue
			}
			desired := desiredChunk{
				sourceKind: source.kind, sourceID: source.stableID(), column: source.column, index: index,
				fingerprint: source.embeddingFingerprint(input), locator: source.locator(), text: input,
			}
			stored := existing[chunkKey(desired.sourceKind, desired.sourceID, desired.column, desired.index)]
			if stored.id != 0 && stored.fingerprint == desired.fingerprint {
				continue
			}
			legacyID := lookup[chunkKey(source.kind, embeddingFingerprint(source.kind, text), "", index)]
			if legacyID == 0 {
				continue
			}
			seeds = append(seeds, legacySeed{legacyID: legacyID, targetID: stored.id, chunk: desired})
			if len(seeds) == legacySeedBatchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("read declared surface for legacy migration %s: %w", database.owner(), err)
	}
	if err := flush(); err != nil {
		return 0, err
	}
	if seeded == 0 {
		return 0, nil
	}
	if _, err := target.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES ('owner',?),('version',?)`,
		database.owner(), f.BuildVersion); err != nil {
		return 0, fmt.Errorf("seal legacy sidecar seed for %s: %w", database.owner(), err)
	}
	if err := sqliteIntegrityCheck(ctx, target); err != nil {
		return 0, fmt.Errorf("verify legacy sidecar seed for %s: %w", database.owner(), err)
	}
	if temporary != "" {
		if err := finishReplacementDatabase(target); err != nil {
			return 0, err
		}
	}
	if err := target.Close(); err != nil {
		return 0, fmt.Errorf("close legacy sidecar seed for %s: %w", database.owner(), err)
	}
	open = false
	if temporary != "" {
		if err := syncFile(temporary); err != nil {
			return 0, fmt.Errorf("sync legacy sidecar seed for %s: %w", database.owner(), err)
		}
		if err := replaceFile(temporary, path); err != nil {
			return 0, fmt.Errorf("publish legacy sidecar seed for %s: %w", database.owner(), err)
		}
	}
	return seeded, nil
}

func copyLegacySeedBatch(ctx context.Context, legacy, target *sql.DB,
	seeds []legacySeed, dimensions int) (int, error) {
	arguments := make([]any, len(seeds))
	placeholders := make([]string, len(seeds))
	for index, seed := range seeds {
		arguments[index] = seed.legacyID
		placeholders[index] = "?"
	}
	rows, err := legacy.QueryContext(ctx, `SELECT rowid,vec_f32(embedding) FROM embeddings WHERE rowid IN (`+
		strings.Join(placeholders, ",")+`)`, arguments...)
	if err != nil {
		return 0, fmt.Errorf("read legacy embeddings: %w", err)
	}
	vectors := make(map[int64][]byte, len(seeds))
	for rows.Next() {
		var id int64
		var vector []byte
		if err := rows.Scan(&id, &vector); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read legacy embedding: %w", err)
		}
		vectors[id] = append([]byte(nil), vector...)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("read legacy embeddings: %w", err)
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	for _, seed := range seeds {
		vector := vectors[seed.legacyID]
		if len(vector) != dimensions*4 {
			return 0, fmt.Errorf("legacy embedding %d has %d bytes, want %d", seed.legacyID, len(vector), dimensions*4)
		}
		where, err := json.Marshal(seed.chunk.locator)
		if err != nil {
			return 0, err
		}
		id := seed.targetID
		if id == 0 {
			result, err := tx.ExecContext(ctx, `INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
				VALUES (?,?,?,?,?,?)`, seed.chunk.sourceKind, seed.chunk.sourceID, seed.chunk.column, seed.chunk.index,
				seed.chunk.fingerprint, string(where))
			if err != nil {
				return 0, fmt.Errorf("write legacy chunk seed: %w", err)
			}
			id, err = result.LastInsertId()
			if err != nil {
				return 0, err
			}
		} else {
			if err := deleteEmbeddings(ctx, tx, id); err != nil {
				return 0, fmt.Errorf("replace legacy chunk seed embeddings: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE chunks SET fingerprint=?,locator=?,updated_at=datetime('now')
				WHERE id=?`, seed.chunk.fingerprint, string(where), id); err != nil {
				return 0, fmt.Errorf("update legacy chunk seed: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO embeddings(rowid,embedding) VALUES (?,?)`, id, vector); err != nil {
			return 0, fmt.Errorf("write legacy float embedding: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ann_embeddings(rowid,embedding)
			VALUES (?,vec_quantize_binary(?))`, id, vector); err != nil {
			return 0, fmt.Errorf("write legacy ANN embedding: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(seeds), nil
}

func (f Federation) notifyLegacySeed(owner string, err error) {
	if f.Notice != nil {
		f.Notice("legacy vector reuse for " + owner + " was skipped: " + err.Error())
	}
}
