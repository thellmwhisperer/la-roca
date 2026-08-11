package search_test

import (
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// Tokenize has to cut exactly where `unicode61 remove_diacritics 2` cuts,
// because the tokens it produces are what the MATCH expression asks the index
// for. SQLite's token characters are the letter categories, ALL the number
// categories and private use; cutting on `unicode.IsDigit` alone kept only the
// decimal digits, so a superscript or a private-use character became a
// separator here and stayed inside the token in the index. A term the index
// holds as one token was then asked for as two, and never matched.
func TestTokenizeCutsWhereTheIndexCuts(t *testing.T) {
	for _, want := range []struct {
		name  string
		text  string
		token []string
	}{
		{name: "decimal digits join their word", text: "roca2 ships", token: []string{"roca2", "ships"}},
		{name: "a superscript is a token character", text: "x² grows", token: []string{"x²", "grows"}},
		{name: "a private use rune is a token character",
			text: "ab apart", token: []string{"ab", "apart"}},
		{name: "punctuation still cuts", text: "one,two", token: []string{"one", "two"}},
		{name: "diacritics fold away", text: "Müller", token: []string{"muller"}},
	} {
		t.Run(want.name, func(t *testing.T) {
			if got := search.Tokenize(want.text); !slices.Equal(got, want.token) {
				t.Errorf("Tokenize(%q) = %q, want %q", want.text, got, want.token)
			}
		})
	}
}
