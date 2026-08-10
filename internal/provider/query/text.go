// Package query builds the prompt the model answers over and renders the FTS
// search the rescue falls back to.
//
// v1 is model-only: every question goes to the model, which generates SQL over
// the SQLite + FTS5 schema (see prompt.go), validated by the gate
// (internal/query/sqlgate). When the model cannot answer, the rescue searches
// the FTS5 index with the question's own words (see fts.go). There is no
// deterministic text processing with no language-specific vocabulary: the utilities
// here are the only residue, and they are language-independent.
package query

import (
	"slices"
	"strings"
	"unicode"
)

// Normalize leaves a question in a comparable shape: lower-cased, no diacritics,
// no punctuation and whitespace collapsed. The FTS index folds identically, so a
// normalized question and the index start from the same tokens.
func Normalize(text string) string { return clean(text, true) }

// Fold strips the diacritics from an already cleaned text. It is the same
// folding the FTS5 tokenizer and the search layer do, kept here so the rescue's
// term and the index agree on what a word is.
func Fold(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		b.WriteRune(fold(r))
	}
	return b.String()
}

// SearchTerm turns a free-form question into the words the FTS rescue searches
// for: folded, substantial and joined by "+". It is language-independent on
// purpose: v1 has no built-in stop-word list, so it keeps the
// tokens that carry the question and drops the ones that do not: anything shorter
// than three letters and anything that is only digits. What noise remains, bm25
// ranks.
func SearchTerm(question string) string {
	var chosen []string
	for _, word := range strings.Fields(clean(question, true)) {
		if len(word) < 3 || isNumber(word) {
			continue
		}
		if !slices.Contains(chosen, word) {
			chosen = append(chosen, word)
		}
		if len(chosen) == maxSearchWords {
			break
		}
	}
	return strings.Join(chosen, "+")
}

// maxSearchWords caps the rescue term. It is generous on purpose: a natural
// question's content word can sit behind several framing words, and capping too
// low drops the very word the rescue needs. The rescue matches with OR, so a
// longer term widens the net rather than tightening it.
const maxSearchWords = 8

// clean lower-cases, turns punctuation into spaces and collapses. With folds
// set, it also strips the diacritics.
func clean(text string, folds bool) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if folds {
				r = fold(r)
			}
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// fold strips the diacritic from the accented letters that appear in the
// corpus. It does not use a full Unicode table because that would add a new
// dependency to a dependency list that is closed on purpose, and it is the same
// table the search layer carries.
func fold(r rune) rune {
	for i, accented := range accentedRunes {
		if accented == r {
			return plainRunes[i]
		}
	}
	return r
}

var (
	accentedRunes = []rune("áàäâãéèëêíìïîóòöôõúùüûñç")
	plainRunes    = []rune("aaaaaeeeeiiiiooooouuuunc")
)

func isNumber(word string) bool {
	return word != "" && strings.IndexFunc(word, func(r rune) bool { return !unicode.IsDigit(r) }) < 0
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}
