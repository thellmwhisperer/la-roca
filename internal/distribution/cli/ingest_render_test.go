package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// The ingest summary counts in prose, and prose counts through axi.Quantity.
// The deferred line was the one count still concatenating a bare number to a
// hardcoded plural noun, so a single held turn read "1 exchanges".
func TestTheDeferredLineCountsHeldExchangesInProse(t *testing.T) {
	for _, want := range []struct {
		held int
		line string
	}{
		{held: 1, line: "1 exchange still being written"},
		{held: 2, line: "2 exchanges still being written"},
	} {
		var output strings.Builder
		renderIngest(&cliEnv{out: &output}, service.IngestResult{
			Result: ingest.Result{ExchangesHeld: want.held},
		})
		if !strings.Contains(output.String(), want.line) {
			t.Errorf("%d held: want %q in\n%s", want.held, want.line, output.String())
		}
	}
}

// One source that answered is "claude-code" on every line of the same run.
// The label was derived from that source's session count, so the live rows
// called it "claude" until the first session landed and "claude-code"
// afterwards: one source under two names inside one report.
func TestASourceKeepsOneNameForTheWholeRun(t *testing.T) {
	if got := ingestSourceLabel("claude"); got != "claude-code" {
		t.Errorf("the claude family is labelled %q, not its roster name", got)
	}
	if got := ingestSourceLabel("codex"); got != "codex" {
		t.Errorf("an unmapped source was renamed to %q", got)
	}
}
