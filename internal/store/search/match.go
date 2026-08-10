// Package search is La Roca's search: the lexical route over a SQLite FTS5
// index, with an honest fall to a plain LIKE scan when the index is not there
// yet.
//
// v1.0 is lexical. There is no second route and no fusion: the index ranks by
// bm25, and that is the answer. Which method ran travels in the answer's
// provenance, because a poor result over the index and a poor one over a LIKE
// are fixed in different ways.
package search

import (
	"strings"
	"unicode"
)

// Search methods, as they are declared in the provenance.
const (
	// MethodLike is the reference floor: a LIKE over every text column, no
	// index. It is the competitor the index is measured against and the route
	// the search falls to when the database is not indexed yet.
	MethodLike = "like"
	// MethodFTS is the SQLite FTS5 full-text index, ranked by bm25.
	MethodFTS = "fts"
)

// MatchAll is how the plan's term joins its words into an FTS5 expression: the
// search requires all of them.
const MatchAll = " AND "

// MatchAny joins the term's words with OR, the lenient form the keyword rescue
// uses: a fallback shares perhaps one word with a memory, so OR finds what AND
// would miss.
const MatchAny = " OR "

// MatchExpression translates the plan's term into an FTS5 MATCH expression.
//
// Every word goes in double quotes, which is the same contract the LIKE it
// replaces used to meet: searching for "long dashes" is not searching for
// "long" or "dashes".
//
// The quotes are not cosmetic. Without them, a term carrying the word "OR"
// would turn the AND into an OR, and one carrying a parenthesis would blow up
// the engine's parser: in FTS5 the search string is a language, and putting user
// text in without quoting it is the same class of failure as concatenating SQL.
func MatchExpression(term, joiner string) string {
	var parts []string
	for _, chunk := range strings.Split(term, "+") {
		// Tokenizing uses the same folding as the index, so what comes out of
		// here is already index tokens.
		for _, token := range Tokenize(chunk) {
			parts = append(parts, `"`+token+`"`)
		}
	}
	return strings.Join(parts, joiner)
}

// Tokenize splits a text into folded words: lower case, no diacritics, cutting
// on everything that is not a letter or a digit.
//
// It is exactly what the `unicode61 remove_diacritics 2` tokenizer does, the one
// the FTS5 index is built with, and exactly the folding `query.Fold` does. That
// what the index stores and what the query asks for start from the same tokens
// is not a happy coincidence: it is what makes the search hit its own index.
func Tokenize(text string) []string {
	var tokens []string
	var current strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(fold(unicode.ToLower(r)))
			continue
		}
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// fold strips the diacritic from the letters of the real corpus, which is
// Spanish and English. It is the same table as query.Fold, duplicated on
// purpose: this package does not depend on the cascade (query imports search,
// not the other way round), and it is four lines.
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
