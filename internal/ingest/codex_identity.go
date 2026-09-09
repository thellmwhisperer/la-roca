package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// reconcileCodexSessionIDs repairs inherited stem siblings before watermarks can
// skip their sources. A common prefix alone is never identity: the stored Codex
// thread and rollout must agree. Parsers and harvest cursors keep source IDs.
func reconcileCodexSessionIDs(ctx context.Context, db Database) error {
	rows, err := queryRows(ctx, db.SQL(), `SELECT session_id, metadata,
			json_extract(metadata, '$.codex_thread_id') AS thread_id
			FROM sessions WHERE source_agent = 'codex' AND json_valid(metadata)
			AND length(session_id) = 36
			AND length(json_extract(metadata, '$.codex_thread_id')) = 36
			AND session_id <> json_extract(metadata, '$.codex_thread_id')
			AND substr(session_id, 1, 35) = substr(json_extract(metadata, '$.codex_thread_id'), 1, 35)
			ORDER BY session_id`)
	if err != nil || len(rows) == 0 {
		return err
	}
	return db.Write(ctx, func(tx *sql.Tx) error {
		pending := map[string]row{}
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return err
		}
		for _, source := range rows {
			// A concurrent ingest may have repaired it while this write waited.
			fresh, err := queryRows(ctx, tx, `SELECT *,
				json_extract(metadata, '$.codex_thread_id') AS thread_id FROM sessions WHERE session_id=?`, source.text("session_id"))
			if err != nil {
				return err
			}
			if len(fresh) == 0 {
				continue
			}
			if fresh[0].text("metadata") != source.text("metadata") {
				return fmt.Errorf("Codex session changed during identity repair")
			}
			if err := mergeCodexSessionID(ctx, tx, fresh[0], pending); err != nil {
				return fmt.Errorf("reconcile Codex session identity: %w", err)
			}
		}
		// Fill envelopes only after every sibling is gone. Updating after each
		// donor can collide with a later donor's exact-payload guard.
		for id, f := range pending {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET project=?, title=?,
				started_at=?, ended_at=?, duration_minutes=?, source_surface=?, metadata=? WHERE session_id=?`,
				f["project"], f["title"], f["started_at"], f["ended_at"], f["duration_minutes"], f["source_surface"], f["metadata"], id); err != nil {
				return err
			}
		}
		return nil
	})
}

func mergeCodexSessionID(ctx context.Context, tx *sql.Tx, source row, pending map[string]row) error {
	oldID, id := source.text("session_id"), source.text("thread_id")
	var incoming map[string]any
	if err := json.Unmarshal([]byte(source.text("metadata")), &incoming); err != nil {
		return err
	}
	path, _ := incoming["codex_rollout_path"].(string)
	if path == "" {
		return nil
	}
	current, exists := pending[id]
	if !exists {
		found, err := queryRows(ctx, tx, `SELECT * FROM sessions WHERE session_id=?`, id)
		if err != nil {
			return err
		}
		if len(found) != 0 {
			current, exists = found[0], true
		}
	}
	metadata := incoming
	if exists {
		var stored map[string]any
		if err := json.Unmarshal([]byte(current.text("metadata")), &stored); err != nil {
			return err
		}
		thread, _ := stored["codex_thread_id"].(string)
		rollout, _ := stored["codex_rollout_path"].(string)
		if current.text("source_agent") != "codex" || (thread != "" && thread != id) || (rollout != "" && rollout != path) {
			return fmt.Errorf("source identity conflicts with existing thread %s", id)
		}
		if err := moveCodexExchangeNumbers(ctx, tx, oldID, id, incoming); err != nil {
			return err
		}
		// Shared source keys keep the already-canonical binding. Their other
		// historical rows remain preserved; identity repair is not child dedup.
		metadata = mergeMetadata(incoming, stored)
	}
	for _, target := range []struct{ table, column string }{
		{"exchanges", "session_id"}, {"tool_uses", "session_id"},
		{"thinking_blocks", "session_id"}, {"memories", "source_session"},
	} {
		if _, err := tx.ExecContext(ctx, "UPDATE "+target.table+" SET "+target.column+" = ? WHERE "+target.column+" = ?", id, oldID); err != nil {
			return err
		}
	}
	// Existing exact-dedup audit references follow their surviving parent. No new
	// remap ledger is needed: the source identity remains in the session metadata.
	var hasRemaps int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_id_remaps'`).Scan(&hasRemaps); err != nil {
		return err
	}
	if hasRemaps != 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE session_id_remaps SET canonical_id=? WHERE canonical_id=?`, id, oldID); err != nil {
			return err
		}
	}
	if !exists {
		pending[id] = source
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET session_id=? WHERE session_id=?`, id, oldID)
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE session_id=?`, oldID); err != nil {
		return err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	current["metadata"] = string(encoded)
	for _, column := range []string{"project", "title", "started_at", "ended_at", "duration_minutes", "source_surface"} {
		if current[column] == nil {
			current[column] = source[column]
		}
	}
	pending[id] = current
	return nil
}

// Numbers are local to a session. Keep each sibling's children together and
// allocate only collisions; NULL remains a session-level call, never an invented
// association with a history prompt. Row IDs, text and error payloads stay intact.
func moveCodexExchangeNumbers(ctx context.Context, tx *sql.Tx, oldID, id string, incoming map[string]any) error {
	rows, err := queryRows(ctx, tx, `SELECT session_id, exchange_number FROM exchanges WHERE session_id IN (?,?)
		UNION SELECT session_id, exchange_number FROM tool_uses WHERE session_id IN (?,?)
		UNION SELECT session_id, exchange_number FROM thinking_blocks WHERE session_id IN (?,?)
		ORDER BY exchange_number DESC`, oldID, id, oldID, id, oldID, id)
	if err != nil {
		return err
	}
	used := map[int]bool{}
	next := 0
	for _, r := range rows {
		if n, ok := r.number("exchange_number"); ok {
			if int(n) > next {
				next = int(n)
			}
			if r.text("session_id") == id {
				used[int(n)] = true
			}
		}
	}
	remapped := map[int]int{}
	for _, r := range rows {
		n, ok := r.number("exchange_number")
		if !ok || r.text("session_id") != oldID || !used[int(n)] {
			continue
		}
		next++
		remapped[int(n)] = next
		for _, table := range []string{"exchanges", "tool_uses", "thinking_blocks"} {
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET exchange_number=? WHERE session_id=? AND exchange_number=?", next, oldID, n); err != nil {
				return err
			}
		}
	}
	ids, _ := incoming["source_exchange_ids"].(map[string]any)
	for key, value := range ids {
		switch v := value.(type) {
		case float64:
			if number, ok := remapped[int(v)]; ok {
				ids[key] = number
			}
		case map[string]any:
			if old, ok := v["exchange_number"].(float64); ok {
				if number, ok := remapped[int(old)]; ok {
					v["exchange_number"] = number
				}
			}
		}
	}
	return nil
}
