package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestRefusalIsRecognizedBeforeSQLParsing(t *testing.T) {
	for _, testCase := range []struct {
		answer string
		want   bool
	}{
		{answer: "REFUSE", want: true},
		{answer: "  refuse;  ", want: true},
		{answer: "```sql\nREFUSE\n```", want: true},
		{answer: "REFUSE because the question is outside the memory database", want: true},
		{answer: "SELECT 'REFUSE' AS decision LIMIT 1"},
		{answer: "REFUSED"},
	} {
		if got := query.IsRefusal(testCase.answer); got != testCase.want {
			t.Errorf("IsRefusal(%q) = %v, want %v", testCase.answer, got, testCase.want)
		}
	}
}
