package vector

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const frontierMetaKey = "corpus_change_frontier"

type changeFrontier struct {
	Sequence int64  `json:"sequence"`
	Token    string `json:"token"`
	Contract string `json:"contract"`
	Model    string `json:"model"`
	changed  map[string]map[string]bool
}

func (d DeclaredCorpus) supportsFrontier() bool {
	if d.Database.Plugin != "roca-corpus" {
		return false
	}
	columns := map[string][]string{
		"sessions":        {"session_id", "title", "project", "started_at"},
		"memories":        {"id", "content", "created_at", "source_session", "project"},
		"exchanges":       {"id", "human_text", "agent_text", "human_timestamp", "agent_timestamp", "session_id"},
		"thinking_blocks": {"id", "full_text", "session_id"},
	}
	for _, table := range d.Database.Tables {
		allowed := columns[table.Name]
		if len(allowed) == 0 || table.IDColumn != allowed[0] {
			return false
		}
		for _, col := range append(append([]string{}, table.TextColumns...), table.TimeColumns...) {
			if !containsString(allowed, col) {
				return false
			}
		}
		if j := table.TimeJoin; j != nil {
			local := "session_id"
			if table.Name == "memories" {
				local = "source_session"
			}
			if j.Table != "sessions" || j.ForeignColumn != "session_id" || j.LocalColumn != local {
				return false
			}
			for _, col := range j.TimeColumns {
				if col != "started_at" {
					return false
				}
			}
		}
	}
	return true
}

func (d DeclaredCorpus) readFrontier(ctx context.Context, store *sql.DB, model string, reembed bool) (*changeFrontier, error) {
	if !d.supportsFrontier() {
		return nil, nil
	}
	db, err := openSQLite(d.opsDatabaseFile(), true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='vector_changes'`).Scan(&present); err != nil {
		return nil, err
	}
	if present == 0 {
		return nil, nil
	}
	next := &changeFrontier{Contract: d.Database.contractFingerprint(), Model: model}
	if err := tx.QueryRowContext(ctx, `SELECT sequence,token FROM vector_changes ORDER BY sequence DESC LIMIT 1`).Scan(&next.Sequence, &next.Token); err != nil {
		return nil, err
	}
	metadata, err := readMetadata(store, frontierMetaKey)
	if err == sql.ErrNoRows {
		return next, nil
	}
	if err != nil {
		return nil, err
	}
	var previous changeFrontier
	if reembed || json.Unmarshal([]byte(metadata[frontierMetaKey]), &previous) != nil || previous.Contract != next.Contract || previous.Model != model {
		return next, nil
	}
	var token string
	err = tx.QueryRowContext(ctx, `SELECT token FROM vector_changes WHERE sequence=?`, previous.Sequence).Scan(&token)
	if err == sql.ErrNoRows || err == nil && token != previous.Token {
		return next, nil
	}
	if err != nil {
		return nil, err
	}
	next.changed = map[string]map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT source_kind,source_id FROM vector_changes WHERE sequence>? AND sequence<=?`, previous.Sequence, next.Sequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, err
		}
		if next.changed[kind] == nil {
			next.changed[kind] = map[string]bool{}
		}
		next.changed[kind][id] = true
	}
	return next, rows.Err()
}

func (f *changeFrontier) retains(chunk storedChunk) bool {
	id, err := url.PathUnescape(strings.TrimPrefix(chunk.sourceID, chunk.sourceKind+"/"))
	return err == nil && !f.changed[chunk.sourceKind][id]
}

func (f *changeFrontier) predicate(table vectorTable) string {
	ids := make([]string, 0, len(f.changed[table.Name]))
	for id := range f.changed[table.Name] {
		ids = append(ids, sqlLiteral(id))
	}
	sort.Strings(ids)
	return fmt.Sprintf("src.%s IN (%s)", quoteIdentifier(table.IDColumn), strings.Join(ids, ","))
}
