package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
)

func TestIngestRowsCarryCountersAndCollapseAway(t *testing.T) {
	var output strings.Builder
	rows := newIngestRows(&output, true)
	rows.update(ingest.SourceProgress{Source: "claude-code", Processed: 1501,
		Total: 1501, Discarded: 3, ElapsedMS: 91234, Done: true})
	rows.draw()
	rows.finish()

	got := output.String()
	for _, want := range []string{"✓ claude-code", "1,501/1,501 files", "3 discarded", "91 s"} {
		if !strings.Contains(got, want) {
			t.Errorf("live source row does not carry %q: %q", want, got)
		}
	}
	if !strings.HasSuffix(got, clearLine) {
		t.Errorf("live rows were not cleared before the summary: %q", got)
	}
}

func TestIngestRowsAreInertOffATerminal(t *testing.T) {
	var output strings.Builder
	rows := newIngestRows(&output, false)
	rows.update(ingest.SourceProgress{Source: "claude", Processed: 1, Total: 1})
	rows.finish()
	if output.Len() != 0 {
		t.Fatalf("plain stream received terminal control bytes: %q", output.String())
	}
}

// The same contract the spinner carries: finish joins the goroutine and must
// survive being called twice. The ingest path guards it by nilling the pointer
// after the first call, but the guard belongs to the object, not to the one
// caller that remembers.
func TestIngestRowsFinishIsSafeToCallTwice(t *testing.T) {
	var buf strings.Builder
	rows := newIngestRows(&buf, true)
	rows.update(ingest.SourceProgress{Source: "claude-code", Processed: 1, Total: 1})

	rows.finish()
	rows.finish()
}
