package search

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/data"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

// search_state keys.
const (
	keyLexical          = "lexical_index"
	keyTokenizer        = "lexical_tokenizer"
	tokenizerGeneration = "unicode61-remove-diacritics-2"
	tokenizerRebuilding = "rebuilding-" + tokenizerGeneration
)

var currentTokenizer = regexp.MustCompile(
	`(?i)tokenize\s*=\s*["']unicode61\s+remove_diacritics\s+2["']`)

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
func Index(ctx context.Context, db *store.DB, progress func(string)) (Report, error) {
	start := time.Now()
	var report Report

	migrated, err := EnsureTokenizer(ctx, db, progress)
	if err != nil {
		return report, err
	}

	built, err := buildLexicalIndex(ctx, db)
	if err != nil {
		return report, err
	}
	report.LexicalBuilt = migrated || built

	report.ElapsedMS = time.Since(start).Milliseconds()
	return report, nil
}

func Rebuild(ctx context.Context, db *store.DB) (Report, error) {
	started := time.Now()
	if err := recreateLexicalTables(ctx, db); err != nil {
		return Report{}, err
	}
	built, err := buildLexicalIndex(ctx, db)
	if err != nil {
		return Report{}, err
	}
	if err := recordTokenizerGeneration(ctx, db); err != nil {
		return Report{}, err
	}
	return Report{LexicalBuilt: built, ElapsedMS: time.Since(started).Milliseconds()}, nil
}

// RebuildSources recreates the declared derived FTS tables without changing
// their source rows.
func RebuildSources(ctx context.Context, db *store.DB, sources []ProofSource) (Report, error) {
	started := time.Now()
	tx, err := db.SQL().BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	for _, source := range sources {
		if len(source.Columns) == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+quoteProofIdentifier(source.Index)); err != nil {
			return Report{}, err
		}
		columns := make([]string, len(source.Columns))
		for index, column := range source.Columns {
			columns[index] = quoteProofIdentifier(column)
		}
		options := "content='" + strings.ReplaceAll(source.Table, "'", "''") + "'"
		if source.IDColumn != "" {
			options += ",content_rowid='" + strings.ReplaceAll(source.IDColumn, "'", "''") + "'"
		}
		statement := fmt.Sprintf(
			`CREATE VIRTUAL TABLE %s USING fts5(%s,%s,tokenize='unicode61 remove_diacritics 2')`,
			quoteProofIdentifier(source.Index), strings.Join(columns, ","), options)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return Report{}, err
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s(%s) VALUES('rebuild')`, quoteProofIdentifier(source.Index),
			quoteProofIdentifier(source.Index))); err != nil {
			return Report{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Report{}, err
	}
	return Report{LexicalBuilt: true, ElapsedMS: time.Since(started).Milliseconds()}, nil
}

// EnsureTokenizer upgrades an existing lexical index to the tokenizer shipped
// by this build. The content tables are never changed: only the four derived FTS
// tables and their state markers are replaced.
//
// A committed rebuilding marker makes an interrupted upgrade resumable. The
// per-table markers written by buildLexicalIndex then skip every table that had
// already committed before the interruption.
func EnsureTokenizer(ctx context.Context, db *store.DB, progress func(string)) (bool, error) {
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		return false, err
	}

	state, err := readState(ctx, db, keyTokenizer, "read the tokenizer generation")
	if err != nil {
		return false, err
	}
	if state == tokenizerGeneration {
		return false, nil
	}
	current, err := tokenizerDefinitionsCurrent(ctx, db)
	if err != nil {
		return false, err
	}
	if current && state == "" {
		lexicalState, err := readState(ctx, db, keyLexical, "read the lexical index state")
		if err != nil {
			return false, err
		}
		built := false
		if lexicalState != "built" {
			built, err = buildLexicalIndex(ctx, db)
			if err != nil {
				return false, err
			}
		}
		if err := recordTokenizerGeneration(ctx, db); err != nil {
			return false, err
		}
		return built, nil
	}

	if progress != nil {
		progress("index: rebuilding for accent-insensitive search; a large corpus can take a few minutes")
	}
	if !current {
		if err := recreateLexicalTables(ctx, db); err != nil {
			return false, err
		}
	}

	built, err := buildLexicalIndex(ctx, db)
	if err != nil {
		return false, err
	}
	if err := recordTokenizerGeneration(ctx, db); err != nil {
		return false, err
	}
	return built || state == tokenizerRebuilding || !current, nil
}

func recordTokenizerGeneration(ctx context.Context, db *store.DB) error {
	err := db.Write(ctx, func(tx *sql.Tx) error {
		return writeState(ctx, tx, keyTokenizer, tokenizerGeneration)
	})
	if err != nil {
		return fmt.Errorf("record the tokenizer generation: %w", err)
	}
	return nil
}

func tokenizerDefinitionsCurrent(ctx context.Context, db *store.DB) (bool, error) {
	current := true
	for _, table := range lexicalTables {
		var ddl string
		err := db.SQL().QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, table,
		).Scan(&ddl)
		if err != nil {
			return false, fmt.Errorf("inspect the tokenizer of %s: %w", table, err)
		}
		if !strings.Contains(strings.ToLower(ddl), "using fts5") {
			return false, fmt.Errorf("inspect the tokenizer of %s: it is not an FTS5 table", table)
		}
		if !currentTokenizer.MatchString(ddl) {
			current = false
		}
	}
	return current, nil
}

func recreateLexicalTables(ctx context.Context, db *store.DB) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		for _, table := range lexicalTables {
			if _, err := tx.ExecContext(ctx, "DROP TABLE "+table); err != nil {
				return fmt.Errorf("drop the old search index %s: %w", table, err)
			}
		}
		if _, err := tx.ExecContext(ctx, data.SearchSchema); err != nil {
			return fmt.Errorf("create the accent-insensitive search index: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM search_state WHERE key = ? OR key LIKE ?`,
			keyLexical, keyLexical+":%",
		); err != nil {
			return fmt.Errorf("reset the lexical index state: %w", err)
		}
		return writeState(ctx, tx, keyTokenizer, tokenizerRebuilding)
	})
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
