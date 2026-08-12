package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func TestInterpretationGuardianOnlyDeletesUnsupportedComparisons(t *testing.T) {
	for _, testCase := range []struct {
		name, text, want string
		columns          []string
	}{
		{
			name: "combined comparison", columns: []string{"name", "count"},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "double comparison", columns: []string{"name", "count"},
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "comparison is the whole answer", columns: []string{"name", "count"},
			text: "More than the next two combined.", want: "",
		},
		{
			name: "explicit comparison column", columns: []string{"name", "combined_total"},
			text: "Alpha is more than the next two combined.",
			want: "Alpha is more than the next two combined.",
		},
		{
			name: "ordinary language", columns: []string{"name", "count"},
			text: "The next two entries are shown below.",
			want: "The next two entries are shown below.",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := query.SanitizeInterpretation(testCase.text, testCase.columns); got != testCase.want {
				t.Fatalf("SanitizeInterpretation() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestRawMultiRowShapeWarnsAgainstInventedComparisons(t *testing.T) {
	if hint := query.InterpretationShapeHint([]string{"name", "count"}, 3); hint == "" {
		t.Fatal("raw multi-row results have no guardian hint")
	}
	if hint := query.InterpretationShapeHint([]string{"name", "ratio"}, 3); hint != "" {
		t.Fatalf("an explicit comparison column was treated as raw rows: %q", hint)
	}
}
