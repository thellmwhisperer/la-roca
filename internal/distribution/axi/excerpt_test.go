package axi_test

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
)

// The rendered table is what a reader attributes a fact to, so a clipped cell
// keeps the subject the row opens with: person B may appear late in person A's
// row without inheriting person A's attributes.
func TestRenderedRowKeepsTheSubjectWithTheMatch(t *testing.T) {
	const aboutPersonA = "Alex Rivera, staff engineer on the synthetic platform team, owns the " +
		"ingest lane, writes the weekly note, reviews the release checklist, and mentors the " +
		"on-call rota; later that quarter Morgan Diaz joined the synthetic launch review as the " +
		"second reader."
	const subject = "Alex Rivera, staff engineer on the synthetic platform team"
	for _, test := range []struct {
		name, term string
		wantMatch  string
	}{
		{"late match", "Morgan Diaz", "Morgan Diaz"},
		{"match inside the subject", "synthetic platform", "synthetic platform"},
		{"no match", "nothing here", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := []map[string]any{{"id": 1, "text": aboutPersonA}}
			got := axi.RowOutput([]string{"id", "text"}, rows, test.term)
			if strings.Contains(got, "\"…") {
				t.Fatalf("RowOutput = %q, the clipped cell dropped its subject", got)
			}
			if !strings.Contains(got, subject) {
				t.Fatalf("RowOutput = %q, want the subject %q", got, subject)
			}
			if test.wantMatch != "" && !strings.Contains(got, test.wantMatch) {
				t.Fatalf("RowOutput = %q, want the match %q", got, test.wantMatch)
			}
		})
	}
}
