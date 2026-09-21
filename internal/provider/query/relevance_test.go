package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestSearchTermKeepsTheSubstantialWordsAndDropsTheRest(t *testing.T) {
	benchCases := []struct{ question, term string }{
		{"naïve Müller façade review 2024?", "naive+muller+facade+review"},
		{"what do we know about ffmpeg", "what+know+about+ffmpeg"},
	}
	for _, c := range benchCases {
		if got := query.SearchTerm(c.question); got != c.term {
			t.Errorf("SearchTerm(%q) = %q, want %q", c.question, got, c.term)
		}
	}
}
