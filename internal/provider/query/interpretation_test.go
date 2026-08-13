package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

// counted is the ordinary shape a comparison is written about: who, and how
// many. The names are the ones the prose below calls them by.
func counted(counts ...any) []map[string]any {
	names := []string{"Alpha", "Beta", "Gamma", "Delta"}
	rows := make([]map[string]any, 0, len(counts))
	for i, count := range counts {
		rows = append(rows, map[string]any{"project": names[i], "sessions": count})
	}
	return rows
}

func TestInterpretationGuardianKeepsOnlyProvenComparisons(t *testing.T) {
	for _, testCase := range []struct {
		name, text, want string
		columns          []string
		rows             []map[string]any
	}{
		{
			name: "the named subject's own numbers contradict it",
			columns: []string{"project", "sessions"}, rows: counted(30, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "the named subject's own numbers prove it",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name: "a named subject below the leader is read at its own rank",
			columns: []string{"project", "sessions"}, rows: counted(100, 50, 20, 10),
			text: "Alpha leads. Beta is more than the next two combined.",
			want: "Alpha leads. Beta is more than the next two combined.",
		},
		{
			name: "a false claim about a named subject is not saved by the leader",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Alpha leads. Beta is more than the next two combined.",
			want: "Alpha leads. Beta is.",
		},
		{
			name: "an unrelated numeric column proves nothing",
			columns: []string{"project", "sessions", "tokens"},
			rows: []map[string]any{
				{"project": "Alpha", "sessions": 30, "tokens": 5000},
				{"project": "Beta", "sessions": 20, "tokens": 100},
				{"project": "Gamma", "sessions": 15, "tokens": 50},
			},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "a coincidental maximum with no subject named proves nothing",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15),
			text: "The leader is more than the next two combined.",
			want: "The leader is.",
		},
		{
			name: "no measured quantity at all",
			columns: []string{"project", "note"},
			rows: []map[string]any{
				{"project": "Alpha", "note": "one"}, {"project": "Beta", "note": "two"},
			},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "numbers the driver returned as text",
			columns: []string{"project", "sessions"}, rows: counted("100", "20", "15"),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name: "double comparison the subject's numbers contradict",
			columns: []string{"project", "sessions"}, rows: counted(30, 25),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name: "double comparison the subject's numbers prove",
			columns: []string{"project", "sessions"}, rows: counted(30, 12),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads — nearly double the next item. Beta follows.",
		},
		{
			name: "comparison is the whole answer",
			columns: []string{"project", "sessions"}, rows: counted(30, 20, 15),
			text: "More than the next two combined.", want: "",
		},
		{
			name: "explicit comparison column", columns: []string{"project", "combined_total"},
			text: "Alpha is more than the next two combined.",
			want: "Alpha is more than the next two combined.",
		},
		{
			name: "ordinary language",
			columns: []string{"project", "sessions"}, rows: counted(30, 20, 15),
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
