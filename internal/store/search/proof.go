package search

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// proofLimit bounds the probe. The question is whether the index answers, not
// how many rows it holds, and a MATCH counted to the end of a real corpus is a
// full scan paid for a yes or no.
const proofLimit = 50

// Proof is the round trip a first run completes before it says word search
// works: a word taken from a row this database already holds, asked back of the
// lexical index, and found there.
//
// The three outcomes are different states, not degrees of the same one. Ready
// is the index answering. Empty is a machine with no agent history yet, which
// is nothing to fix. Neither of those, and the index did not answer for text it
// is supposed to hold, which is the one the operator has to see.
type Proof struct {
	Ready   bool   `json:"ready"`
	Word    string `json:"word,omitempty"`
	Matches int    `json:"matches"`
	// Capped says the count stopped at the probe's ceiling instead of reaching
	// the end, so the number read as "at least this many".
	Capped bool   `json:"capped,omitempty"`
	Empty  bool   `json:"empty,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// proofSource is one indexed text column and the FTS table that carries it.
type proofSource struct {
	table, column, index string
}

var proofSources = []proofSource{
	{"memories", "content", "memories_fts"},
	{"exchanges", "human_text", "exchanges_fts"},
	{"sessions", "title", "sessions_fts"},
	{"thinking_blocks", "full_text", "thinking_fts"},
}

// Prove asks the lexical index for a word this database already stores.
//
// It reads each indexed column newest first until one yields a word and never
// depends on the corpus being in any particular language: the word comes out
// of the data itself.
func Prove(ctx context.Context, db *store.DB) (Proof, error) {
	if db == nil {
		return Proof{}, fmt.Errorf("proving word search needs a database")
	}
	hasRows := false
	for _, source := range proofSources {
		word, rows, err := newestWord(ctx, db, source)
		if err != nil {
			return Proof{}, err
		}
		hasRows = hasRows || rows
		if word == "" {
			continue
		}
		matches, err := countMatches(ctx, db, source.index, word)
		if err != nil {
			return Proof{Word: word, Reason: err.Error()}, nil
		}
		if matches == 0 {
			return Proof{Word: word, Reason: fmt.Sprintf(
				"the word index did not answer for %q, a word %s already holds",
				word, source.table)}, nil
		}
		return Proof{Ready: true, Word: word, Matches: matches,
			Capped: matches >= proofLimit}, nil
	}
	if hasRows {
		return Proof{Reason: "agent history is present but contains no searchable words"}, nil
	}
	return EmptyProof(), nil
}

// EmptyProof is the answer for a machine that has no agent history yet. Nothing
// to search is a fact about the machine, not a fault in the index, and a caller
// proving several databases needs one wording for it.
func EmptyProof() Proof {
	return Proof{Empty: true,
		Reason: "there is no agent history on this machine to search yet"}
}

func newestWord(ctx context.Context, db *store.DB, source proofSource) (string, bool, error) {
	statement := fmt.Sprintf(
		`SELECT %[1]s FROM %[2]s WHERE %[1]s IS NOT NULL AND %[1]s <> '' ORDER BY rowid DESC`,
		source.column, source.table)
	rows, err := db.SQL().QueryContext(ctx, statement)
	if err != nil {
		return "", false, fmt.Errorf("read %s rows to search for: %w", source.table, err)
	}
	defer rows.Close()
	hasRows := false
	for rows.Next() {
		hasRows = true
		var text string
		if err := rows.Scan(&text); err != nil {
			return "", hasRows, fmt.Errorf("read a %s row to search for: %w", source.table, err)
		}
		if word := probeWord(text); word != "" {
			return word, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", hasRows, fmt.Errorf("read %s rows to search for: %w", source.table, err)
	}
	return "", hasRows, nil
}

func countMatches(ctx context.Context, db *store.DB, index, word string) (int, error) {
	statement := fmt.Sprintf(
		`SELECT COUNT(*) FROM (SELECT rowid FROM %[1]s WHERE %[1]s MATCH ? LIMIT %[2]d)`,
		index, proofLimit)
	var matches int
	if err := db.SQL().QueryRowContext(ctx, statement,
		MatchExpression(word, MatchAll)).Scan(&matches); err != nil {
		return 0, fmt.Errorf("search the word index for %q: %w", word, err)
	}
	return matches, nil
}

// probeWord is the longest word of the text that is worth asking for: short
// tokens and bare numbers match half the corpus and prove nothing about the
// index. It falls back to the longest token there is rather than give up on a
// row whose every word is short.
func probeWord(text string) string {
	best, fallback := "", ""
	for _, token := range Tokenize(text) {
		if len([]rune(token)) > len([]rune(fallback)) {
			fallback = token
		}
		if len([]rune(token)) < 4 || !strings.ContainsFunc(token, unicode.IsLetter) {
			continue
		}
		if len([]rune(token)) > len([]rune(best)) {
			best = token
		}
	}
	if best != "" {
		return best
	}
	return fallback
}
