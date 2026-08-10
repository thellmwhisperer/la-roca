package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
)

func TestIngestRowsCarryCountersAndCollapseAway(t *testing.T) {
	previousTick := spinnerTick
	spinnerTick = time.Millisecond
	t.Cleanup(func() { spinnerTick = previousTick })
	var output strings.Builder
	rows := newIngestRows(&output, true)
	rows.update(ingest.SourceProgress{Source: "claude-code", Processed: 1501,
		Total: 1501, Discarded: 3, ElapsedMS: 91234, Done: true})
	time.Sleep(3 * spinnerTick)
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
