// Package opsvector remaps the ops vector sidecar after memory id compact.
package opsvector

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	_ "modernc.org/sqlite"
)

const memoriesKind = "memories"

type mapping struct {
	oldRaw, newRaw, oldStable, newStable string
}

// SidecarPath is the adjacent vector database for one plugin data file.
func SidecarPath(databasePath string) string {
	extension := filepath.Ext(databasePath)
	return strings.TrimSuffix(databasePath, extension) + ".vector" + extension
}

// HasStaleLegacyIDs reports sidecar chunks that still name a compacted
// memories.legacy_id instead of the current id.
func HasStaleLegacyIDs(opsPath string) (bool, error) {
	n, err := remap(opsPath, true)
	return n > 0, err
}

// RemapLegacyIDs rewrites sidecar source_id, raw_source_id, and locator
// identity from memories.legacy_id onto the current id. Embeddings stay.
func RemapLegacyIDs(opsPath string) (int, error) {
	return remap(opsPath, false)
}

func remap(opsPath string, detectOnly bool) (int, error) {
	if strings.TrimSpace(opsPath) == "" {
		return 0, nil
	}
	absolute, err := filepath.Abs(opsPath)
	if err != nil {
		return 0, fmt.Errorf("resolve ops database: %w", err)
	}
	if _, err := os.Stat(absolute); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect ops database: %w", err)
	}
	sidecar := SidecarPath(absolute)
	if _, err := os.Stat(sidecar); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("inspect ops vector sidecar: %w", err)
	}
	source, err := openSQLite(absolute, true)
	if err != nil {
		return 0, fmt.Errorf("open ops database: %w", err)
	}
	defer source.Close()
	var legacyColumn int
	if err := source.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='legacy_id'`).
		Scan(&legacyColumn); err != nil {
		return 0, fmt.Errorf("inspect memories.legacy_id: %w", err)
	}
	if legacyColumn == 0 {
		return 0, nil
	}
	rows, err := source.Query(`SELECT CAST(id AS TEXT), CAST(legacy_id AS TEXT) FROM memories
		WHERE legacy_id IS NOT NULL AND CAST(id AS TEXT) <> CAST(legacy_id AS TEXT)`)
	if err != nil {
		return 0, fmt.Errorf("read compacted memory ids: %w", err)
	}
	var mappings []mapping
	for rows.Next() {
		var item mapping
		if err := rows.Scan(&item.newRaw, &item.oldRaw); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan compacted memory ids: %w", err)
		}
		item.oldStable = memoriesKind + "/" + url.PathEscape(item.oldRaw)
		item.newStable = memoriesKind + "/" + url.PathEscape(item.newRaw)
		mappings = append(mappings, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(mappings) == 0 {
		return 0, nil
	}

	store, err := openSQLite(sidecar, detectOnly)
	if err != nil {
		return 0, fmt.Errorf("open ops vector sidecar: %w", err)
	}
	defer store.Close()
	var chunks int
	if err := store.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='chunks'`).
		Scan(&chunks); err != nil {
		return 0, fmt.Errorf("inspect sidecar chunks: %w", err)
	}
	if chunks == 0 {
		return 0, nil
	}
	if detectOnly {
		return countStaleChunks(store, mappings)
	}
	return applyMappings(store, mappings)
}

func countStaleChunks(store *sql.DB, mappings []mapping) (int, error) {
	stale := 0
	for _, item := range mappings {
		var n int
		if err := store.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind=? AND source_id IN (?,?)`,
			memoriesKind, item.oldStable, item.oldRaw).Scan(&n); err != nil {
			return 0, fmt.Errorf("count stale sidecar chunks: %w", err)
		}
		stale += n
	}
	return stale, nil
}

func applyMappings(store *sql.DB, mappings []mapping) (int, error) {
	tx, err := store.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin sidecar id remap: %w", err)
	}
	defer tx.Rollback()
	var sources int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sources'`).
		Scan(&sources); err != nil {
		return 0, fmt.Errorf("inspect sidecar sources: %w", err)
	}
	changed := 0
	for _, item := range mappings {
		n, err := remapOne(tx, item, sources > 0)
		if err != nil {
			return 0, err
		}
		changed += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit sidecar id remap: %w", err)
	}
	return changed, nil
}

func remapOne(tx *sql.Tx, item mapping, hasSources bool) (int, error) {
	var present int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind=? AND source_id=?`,
		memoriesKind, item.newStable).Scan(&present); err != nil {
		return 0, fmt.Errorf("inspect remapped sidecar chunks: %w", err)
	}
	if present > 0 {
		result, err := tx.Exec(`DELETE FROM chunks WHERE source_kind=? AND source_id IN (?,?)`,
			memoriesKind, item.oldStable, item.oldRaw)
		if err != nil {
			return 0, fmt.Errorf("drop stale sidecar chunks: %w", err)
		}
		n, _ := result.RowsAffected()
		if hasSources {
			if _, err := tx.Exec(`DELETE FROM sources WHERE source_kind=? AND (source_id IN (?,?) OR raw_source_id IN (?,?))`,
				memoriesKind, item.oldStable, item.oldRaw, item.oldRaw, item.oldStable); err != nil {
				return 0, fmt.Errorf("drop stale sidecar sources: %w", err)
			}
		}
		return int(n), nil
	}
	result, err := tx.Exec(`UPDATE chunks SET source_id=?
		WHERE source_kind=? AND source_id IN (?,?)`,
		item.newStable, memoriesKind, item.oldStable, item.oldRaw)
	if err != nil {
		return 0, fmt.Errorf("remap sidecar chunk source_id: %w", err)
	}
	n, _ := result.RowsAffected()
	if err := remapLocators(tx, item); err != nil {
		return 0, err
	}
	if hasSources {
		if _, err := tx.Exec(`UPDATE sources SET source_id=?, raw_source_id=?
			WHERE source_kind=? AND (source_id IN (?,?) OR raw_source_id IN (?,?))`,
			item.newStable, item.newRaw, memoriesKind,
			item.oldStable, item.oldRaw, item.oldRaw, item.oldStable); err != nil {
			return 0, fmt.Errorf("remap sidecar sources: %w", err)
		}
	}
	return int(n), nil
}

func remapLocators(tx *sql.Tx, item mapping) error {
	rows, err := tx.Query(`SELECT id, locator FROM chunks WHERE source_kind=? AND source_id=?`,
		memoriesKind, item.newStable)
	if err != nil {
		return fmt.Errorf("read sidecar locators: %w", err)
	}
	defer rows.Close()
	type update struct {
		id      int64
		locator string
	}
	var updates []update
	for rows.Next() {
		var row update
		if err := rows.Scan(&row.id, &row.locator); err != nil {
			return fmt.Errorf("scan sidecar locator: %w", err)
		}
		rewritten, ok, err := rewriteLocator(row.locator, item)
		if err != nil {
			return err
		}
		if ok {
			updates = append(updates, update{id: row.id, locator: rewritten})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range updates {
		if _, err := tx.Exec(`UPDATE chunks SET locator=? WHERE id=?`, row.locator, row.id); err != nil {
			return fmt.Errorf("rewrite sidecar locator: %w", err)
		}
	}
	return nil
}

func rewriteLocator(raw string, item mapping) (string, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return raw, false, nil
	}
	var locator map[string]any
	if err := json.Unmarshal([]byte(raw), &locator); err != nil {
		return raw, false, nil
	}
	sourceID, _ := locator["source_id"].(string)
	if sourceID != item.oldRaw && sourceID != item.oldStable {
		return raw, false, nil
	}
	locator["source_id"] = item.newRaw
	locator["identity"] = incrementality.ContentFingerprint(strings.Join([]string{memoriesKind, item.newRaw}, "\x00"))
	encoded, err := json.Marshal(locator)
	if err != nil {
		return "", false, fmt.Errorf("encode remapped locator: %w", err)
	}
	return string(encoded), true, nil
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(absolute)
	if readOnly {
		dsn += "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
