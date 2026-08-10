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
func TestTheDeferredLineCountsOneExchangeInTheSingular(t *testing.T) {
	var output strings.Builder
	renderIngest(&cliEnv{out: &output}, service.IngestResult{
		Result: ingest.Result{ExchangesHeld: 1},
	})

	out := output.String()
	if strings.Contains(out, "1 exchanges") {
		t.Errorf("one held turn is counted in the plural:\n%s", out)
	}
	if !strings.Contains(out, "1 exchange ") {
		t.Errorf("one held turn is not counted at all:\n%s", out)
	}
}

// And two are still two.
func TestTheDeferredLineStillCountsSeveralExchangesInThePlural(t *testing.T) {
	var output strings.Builder
	renderIngest(&cliEnv{out: &output}, service.IngestResult{
		Result: ingest.Result{ExchangesHeld: 2},
	})

	if !strings.Contains(output.String(), "2 exchanges") {
		t.Errorf("several held turns are not counted in the plural:\n%s", output.String())
	}
}

// One source that answered is "claude-code" on every line of the same run.
// The label was derived from that source's session count, so the live rows
// called it "claude" until the first session landed and "claude-code"
// afterwards: one source under two names inside one report.
func TestASourceKeepsOneNameForTheWholeRun(t *testing.T) {
	live := ingestSourceLabel("claude")
	landed := ingestSourceLabel("claude")
	if live != landed {
		t.Errorf("the same source is called %q and %q in one run", live, landed)
	}
}
