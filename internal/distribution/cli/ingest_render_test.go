package cli

import (
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
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
		}, false)
		if !strings.Contains(output.String(), want.line) {
			t.Errorf("%d held: want %q in\n%s", want.held, want.line, output.String())
		}
	}
}

func TestMessageCoverageReportsTheConversionRatioAndReasons(t *testing.T) {
	var output strings.Builder
	renderIngest(&cliEnv{out: &output}, service.IngestResult{Result: ingest.Result{
		MessageCoverage: map[string]parsers.MessageCoverage{
			"opencode": {
				Seen: 5, Converted: 3,
				Skipped: map[string]int{
					"assistant message still being written": 1,
					"unsupported message role: system":      1,
				},
			},
		},
	}}, false)
	for _, want := range []string{
		"coverage: opencode messages seen=5 converted=3 skipped=2",
		"skipped: assistant message still being written · 1",
		"skipped: unsupported message role: system · 1",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("want %q in\n%s", want, output.String())
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

// The default summary is what an operator reads after a real ingest, and a real
// ingest of a large corpus leaves hundreds of thousands of runtime records unread
// by design. Printing one line per record turned a healthy run into a wall of
// absolute paths that read as failure.
func TestTheDefaultSummaryCollapsesWhatWasLeftOut(t *testing.T) {
	report := service.IngestResult{Result: ingest.Result{
		ErrorDetails: []ingest.Failure{{
			Path: "/somewhere/private/broken.jsonl", Parser: "codex_session",
			Reason: "fingerprint: open /somewhere/private/broken.jsonl: permission denied; " +
				"sidecar /another/private/paired.json: unavailable",
		}},
		RecordsExcluded:  9,
		RecordsDiscarded: 2,
		DiscardSummary: []ingest.DiscardCategory{
			{Reason: "codex runtime event not ingested: token_count", Count: 9, ByDesign: true},
			{Reason: "invalid JSON: unexpected end of input", Count: 2},
		},
		DiscardDetails: []ingest.DiscardDetail{
			{Path: "/somewhere/private/runtime.jsonl", Parser: "codex_session",
				Record: 3, Reason: "runtime event", ByDesign: true},
			{Path: "/somewhere/private/rollout.jsonl", Parser: "codex_session",
				Record: 4, Reason: "invalid JSON: unexpected end of input"},
		},
	}}

	for _, want := range []struct {
		verbose bool
		present []string
		absent  []string
	}{
		{
			verbose: false,
			present: []string{
				"error: broken.jsonl (codex_session): fingerprint: open broken.jsonl: " +
					"permission denied; sidecar paired.json: unavailable",
				"excluded: 9 records left out by design",
				"· codex runtime event not ingested: token_count · 9",
				"discards: 2 records could not be read",
				"· invalid JSON: unexpected end of input · 2",
				"roca ingest --verbose",
			},
			absent: []string{"/somewhere/private/rollout.jsonl", "/somewhere/private/runtime.jsonl",
				"/somewhere/private/broken.jsonl", "/another/private/paired.json"},
		},
		{
			verbose: true,
			present: []string{"/somewhere/private/rollout.jsonl", "/somewhere/private/broken.jsonl",
				"fingerprint: open /somewhere/private/broken.jsonl: permission denied",
				"excluded: /somewhere/private/runtime.jsonl", "discard: /somewhere/private/rollout.jsonl",
				"excluded: 9 records left out by design"},
			absent: []string{"roca ingest --verbose"},
		},
	} {
		var output strings.Builder
		renderIngest(&cliEnv{out: &output}, report, want.verbose)
		for _, line := range want.present {
			if !strings.Contains(output.String(), line) {
				t.Errorf("verbose=%t: want %q in\n%s", want.verbose, line, output.String())
			}
		}
		for _, line := range want.absent {
			if strings.Contains(output.String(), line) {
				t.Errorf("verbose=%t: want %q absent from\n%s", want.verbose, line, output.String())
			}
		}
	}
}

// A source row that has nothing to complain about does not carry a zero for it:
// "0 discarded" on every healthy row is what taught an operator to read the
// summary looking for bad news.
func TestAHealthySourceRowCarriesNoDiscardCount(t *testing.T) {
	var output strings.Builder
	renderIngestSources(&cliEnv{out: &output}, service.IngestResult{Result: ingest.Result{
		Sources:     map[string]*ingest.Counts{"codex": {Sessions: 2, Exchanges: 7}},
		SourceStats: map[string]*ingest.SourceStats{"codex": {Read: 3}},
	}})
	if strings.Contains(output.String(), "discarded") {
		t.Errorf("a healthy source row mentions discards:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "7 exchanges") {
		t.Errorf("the source row lost its ingested counts:\n%s", output.String())
	}
}

// A store-backed reader reports what it saw before normalization, so an
// operator can tell "converted everything" from "only looked at part of it".
func TestAStoreBackedSourceRowShowsWhatItSaw(t *testing.T) {
	var output strings.Builder
	renderIngestSources(&cliEnv{out: &output}, service.IngestResult{Result: ingest.Result{
		Sources:     map[string]*ingest.Counts{"hermes": {Sessions: 2, Exchanges: 7}},
		SourceStats: map[string]*ingest.SourceStats{"hermes": {Read: 1}},
		Seen:        map[string]parsers.Seen{"hermes": {Sessions: 2, Messages: 28152}},
	}})
	if !strings.Contains(output.String(), "saw 2 sessions") ||
		!strings.Contains(output.String(), "28,152 messages") {
		t.Errorf("the source row lost its seen counts:\n%s", output.String())
	}
}

func TestASourceRowDoesNotCallFingerprintErrorsSkipped(t *testing.T) {
	var output strings.Builder
	renderIngestSources(&cliEnv{out: &output}, service.IngestResult{Result: ingest.Result{
		Sources: map[string]*ingest.Counts{"pi": {}},
		SourceStats: map[string]*ingest.SourceStats{"pi": {
			Processed: 3, Read: 1, FilesErrored: 1,
		}},
	}})
	for _, want := range []string{"3 files seen", "1 parsed", "1 skipped", "1 error"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("source row does not carry %q: %q", want, output.String())
		}
	}
}

func TestCoverageRendersOneSummaryBlockAndHidesPathsUntilVerbose(t *testing.T) {
	report := service.IngestResult{Result: ingest.Result{Coverage: ingest.CoverageReport{
		Files: ingest.FileCoverage{Seen: 12, Claimed: 9, Ingested: 7, Skipped: 5,
			Skips: []ingest.CoverageCategory{{Reason: "unchanged fingerprint", Count: 2}}},
		Gaps:    []ingest.CoverageCategory{{Reason: "Claude memory manifest link is absent from disk", Count: 1}},
		Details: []ingest.CoverageDetail{{Path: "/private/missing.md", Reason: "Claude memory manifest link is absent from disk"}},
		OpenCode: ingest.OpenCodeCoverage{Store: map[string]int{"sessions": 8, "messages": 21},
			Extracted: map[string]int{"sessions": 7, "exchanges": 13}},
	}}}
	for _, test := range []struct {
		verbose bool
		path    bool
	}{
		{verbose: false, path: false},
		{verbose: true, path: true},
	} {
		var output strings.Builder
		renderIngest(&cliEnv{out: &output}, report, test.verbose)
		text := output.String()
		for _, want := range []string{
			"coverage: 12 seen · 9 claimed · 7 ingested · 5 skipped",
			"skipped: unchanged fingerprint · 2",
			"gap: Claude memory manifest link is absent from disk · 1",
			"opencode: store messages=21 sessions=8 · extracted exchanges=13 sessions=7",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("verbose=%t: want %q in\n%s", test.verbose, want, text)
			}
		}
		if strings.Contains(text, "/private/missing.md") != test.path {
			t.Errorf("verbose=%t: path visibility is wrong in\n%s", test.verbose, text)
		}
	}
}
