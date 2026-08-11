package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// search_state keys.
const keyLexical = "lexical_index"

// Report is what the indexing did, so that whoever calls it can say so.
type Report struct {
	LexicalBuilt bool  `json:"lexical_built"`
	ElapsedMS    int64 `json:"elapsed_ms"`
}

// Index leaves the database ready to search: it builds the lexical index if it
// was never built.
//
// It is incremental and resumable by construction. The lexical index maintains
// itself after the first time, because the triggers live in the database and
// anybody who writes fires them, this binary or not.
func Index(ctx context.Context, db *store.DB) (Report, error) {
	start := time.Now()
	var report Report

	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		return report, err
	}

	built, err := buildLexicalIndex(ctx, db)
	if err != nil {
		return report, err
	}
	report.LexicalBuilt = built

	report.ElapsedMS = time.Since(start).Milliseconds()
	return report, nil
}

// readState reads one of the index's own markers. A table that is not there yet
// and a row that is not there yet are the same answer: nothing has been indexed,
// which is a state and not a failure.
func readState(ctx context.Context, db *store.DB, key, doing string) (string, error) {
	var value string
	err := db.SQL().QueryRowContext(ctx,
		`SELECT value FROM search_state WHERE key = ?`, key).Scan(&value)
	if err == nil || err == sql.ErrNoRows || strings.Contains(err.Error(), "no such table") {
		return value, nil
	}
	return "", fmt.Errorf("%s: %w", doing, err)
}

func writeState(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO search_state (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value)
	return err
}

// lexicalTables are the four full-text tables the first build fills, in the order
// it fills them.
var lexicalTables = []string{"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts"}

// buildLexicalIndex fills the full-text index the first time. From then on the
// triggers maintain it, so this does not happen again.
//
// Each table records its own completion, so a run that dies on a later table
// resumes from THAT table instead of rebuilding the ones already done. On the
// real database the exchanges rebuild takes about half a minute and holds the
// write lock while it runs, so repeating it after an unrelated failure is a cost
// the operator pays for nothing.
//
// This is progress, not exclusion: two builders at once still each do the work,
// and they converge because an FTS `rebuild` is idempotent.
func buildLexicalIndex(ctx context.Context, db *store.DB) (bool, error) {
	state, err := readState(ctx, db, keyLexical, "read the lexical index state")
	if err != nil {
		return false, err
	}
	if state == "built" {
		return false, nil
	}

	// Each table in its own transaction: on the real database, exchanges takes
	// half a minute, and putting all four in a single one would leave any other
	// process waiting on the write lock for that whole time.
	for _, table := range lexicalTables {
		done, err := readState(ctx, db, tableKey(table),
			"read the lexical index state of "+table)
		if err != nil {
			return false, err
		}
		if done == "built" {
			continue
		}
		stmt := fmt.Sprintf("INSERT INTO %s(%s) VALUES ('rebuild')", table, table)
		// The rebuild and its record commit together. Writing the marker in a
		// second transaction would let a crash between the two claim a table that
		// was never rebuilt, which is the one mistake this must not make.
		err = db.Write(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
			return writeState(ctx, tx, tableKey(table), "built")
		})
		if err != nil {
			return false, fmt.Errorf("build the lexical index of %s: %w", table, err)
		}
	}

	return true, db.Write(ctx, func(tx *sql.Tx) error {
		return writeState(ctx, tx, keyLexical, "built")
	})
}

// tableKey is where one table's completion is recorded, under the same prefix as
// the terminal marker so the family reads as one.
func tableKey(table string) string { return keyLexical + ":" + table }
