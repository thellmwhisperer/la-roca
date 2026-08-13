package query_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/query"
)

// counted is the ordinary shape a comparison is written about: who, and how
// many. The names are the ones the prose below calls them by.
func counted(counts ...any) []map[string]any { return measured("sessions", counts...) }

// measured is the same shape under the column name the model chose to give it,
// which is what an alias attack varies.
func measured(column string, values ...any) []map[string]any {
	names := []string{"Alpha", "Beta", "Gamma", "Delta"}
	rows := make([]map[string]any, 0, len(values))
	for i, value := range values {
		rows = append(rows, map[string]any{"project": names[i], column: value})
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
			name:    "the named subject's own numbers contradict it",
			columns: []string{"project", "sessions"}, rows: counted(30, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name:    "the named subject's own numbers prove it",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name:    "a named subject below the leader is read at its own rank",
			columns: []string{"project", "sessions"}, rows: counted(100, 50, 20, 10),
			text: "Alpha leads. Beta is more than the next two combined.",
			want: "Alpha leads. Beta is more than the next two combined.",
		},
		{
			name:    "a false claim about a named subject is not saved by the leader",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Alpha leads. Beta is more than the next two combined.",
			want: "Alpha leads.",
		},
		{
			name:    "a deletion mid-sentence takes the clause that leaned on it",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Alpha leads, Beta is more than the next two combined, and Gamma trails.",
			want: "Alpha leads, and Gamma trails.",
		},
		{
			name:    "the sentences around a deleted one are left as they were",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Alpha leads. Beta is more than the next two combined. Gamma trails.",
			want: "Alpha leads. Gamma trails.",
		},
		{
			name:    "a remainder left hanging from a conjunction goes with its sentence",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Beta is more than the next two combined, and Gamma trails. Alpha leads.",
			want: "Alpha leads.",
		},
		{
			name:    "a deleted sentence that had a line to itself takes the line",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15, 10),
			text: "Alpha leads.\nBeta is more than the next two combined.\nGamma trails.",
			want: "Alpha leads.\nGamma trails.",
		},
		{
			name:    "an unrelated numeric column proves nothing",
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
			name:    "a coincidental maximum with no subject named proves nothing",
			columns: []string{"project", "sessions"}, rows: counted(100, 20, 15),
			text: "The leader is more than the next two combined.",
			want: "",
		},
		{
			name:    "no measured quantity at all",
			columns: []string{"project", "note"},
			rows: []map[string]any{
				{"project": "Alpha", "note": "one"}, {"project": "Beta", "note": "two"},
			},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name:    "numbers the driver returned as text",
			columns: []string{"project", "sessions"}, rows: counted("100", "20", "15"),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name:    "double comparison the subject's numbers contradict",
			columns: []string{"project", "sessions"}, rows: counted(30, 25),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name:    "double comparison the subject's numbers prove",
			columns: []string{"project", "sessions"}, rows: counted(30, 12),
			text: "Alpha leads — nearly double the next item. Beta follows.",
			want: "Alpha leads — nearly double the next item. Beta follows.",
		},
		{
			name:    "comparison is the whole answer",
			columns: []string{"project", "sessions"}, rows: counted(30, 20, 15),
			text: "More than the next two combined.", want: "",
		},
		{
			name:    "a column the model called combined does not vouch for its own claim",
			columns: []string{"project", "combined_total"}, rows: measured("combined_total", 30, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name:    "a column the model called ratio is still read as the measure",
			columns: []string{"project", "ratio"}, rows: measured("ratio", 100, 20, 15),
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads, more than the next two combined. Beta follows.",
		},
		{
			name:    "an alias beside the real measure leaves the quantity ambiguous",
			columns: []string{"project", "sessions", "pct"},
			rows: []map[string]any{
				{"project": "Alpha", "sessions": 100, "pct": 1},
				{"project": "Beta", "sessions": 20, "pct": 1},
				{"project": "Gamma", "sessions": 15, "pct": 1},
			},
			text: "Alpha leads, more than the next two combined. Beta follows.",
			want: "Alpha leads. Beta follows.",
		},
		{
			name:    "ordinary language",
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
	if hint := query.InterpretationShapeHint(3); hint == "" {
		t.Fatal("rows a comparison could be invented about carry no warning")
	}
	if hint := query.InterpretationShapeHint(1); hint != "" {
		t.Fatalf("a single row was warned about cross-row arithmetic: %q", hint)
	}
}
