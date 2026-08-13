package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

func rowsOf(counts ...any) []map[string]any {
	rows := make([]map[string]any, 0, len(counts))
	for i, count := range counts {
		rows = append(rows, map[string]any{
			"name": string(rune('A' + i)), "count": count,
		})
	}
	return rows
}

func TestInterpretationGuardianOnlyDeletesUnsupportedComparisons(t *testing.T) {
	for _, testCase := range []struct {
		name, text, want string
		columns          []string
		rows             []map[string]any
	}{
		{
			name: "combined comparison the rows contradict", columns: []string{"name", "count"},
			rows: rowsOf(30, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "combined comparison the rows bear out", columns: []string{"name", "count"},
			rows: rowsOf(100, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name: "combined comparison with no evidence at all", columns: []string{"name", "text"},
			rows: []map[string]any{{"name": "Alpha", "text": "one"}, {"name": "Beta", "text": "two"}},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "numbers the driver returned as text", columns: []string{"name", "count"},
			rows: rowsOf("100", "20", "15"),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name: "double comparison the rows contradict", columns: []string{"name", "count"},
			rows: rowsOf(30, 25),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "double comparison the rows bear out", columns: []string{"name", "count"},
			rows: rowsOf(30, 12),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads — nearly double the next item. Beta follows.",
		},
		{
			name: "comparison is the whole answer", columns: []string{"name", "count"},
			rows: rowsOf(30, 20, 15),
			text: "More than the next two combined.", want: "",
		},
		{
			name: "explicit comparison column", columns: []string{"name", "combined_total"},
			text: "Alpha is more than the next two combined.",
			want: "Alpha is more than the next two combined.",
		},
		{
			name: "ordinary language", columns: []string{"name", "count"},
			rows: rowsOf(30, 20, 15),
			text: "The next two entries are shown below.",
			want: "The next two entries are shown below.",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := query.SanitizeInterpretation(testCase.text, testCase.columns, testCase.rows)
			if got != testCase.want {
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

// A result the guardian can never rewrite must say so, because that is what
// lets its prose reach the operator as it is written instead of at the end.
func TestOnlyASanitizableShapeHasToBeHeldBack(t *testing.T) {
	if !query.InterpretationMayBeSanitized([]string{"name", "count"}) ||
		query.InterpretationMayBeSanitized([]string{"name", "ratio"}) {
		t.Fatal("the buffering decision does not follow the guardian's own reach")
	}
}
