/*
@overview Contracts golden-bench execution, resilience, degradation accounting, and human output. ~175 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestRunPassesEveryCaseThroughEveryCompetitor
	2. Read the failure and degradation tests
	3. Finish at TestWriteEmitsTheCompactTable

	MAIN FLOW
	---------
	testRoca -> bench.Run -> scores and verdicts -> bench.Write

	PUBLIC API
	----------
	None; this file tests the bench package.

	INTERNALS
	---------
	testRoca, benchOfTwo, four Test* contracts

@exports
@deps bytes/context/fmt/strings/testing, internal/bench/search
*/
package bench_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/bench"
	"github.com/thellmwhisperer/la-roca/internal/search"
)

// -- 1/4 HELPER · testRoca and benchOfTwo --

// testRoca answers whatever it is told per method, so the runner can be measured
// without a database.
type testRoca struct {
	byMethod map[string]bench.Observed
	failure  error
	calls    int
}

func (r *testRoca) Ask(_ context.Context, _, method string) (bench.Observed, error) {
	r.calls++
	if r.failure != nil {
		return bench.Observed{}, r.failure
	}
	return r.byMethod[method], nil
}

func benchOfTwo() bench.Bench {
	return bench.Bench{Version: 1, Cases: []bench.Case{
		{ID: "uno", Question: "q1", ExpectRowsContain: []string{"sentinel"}},
		{ID: "dos", Question: "q2", ExpectMinRows: 1},
	}}
}

// -/ 1/4

// -- 2/4 CORE · TestRunPassesEveryCaseThroughEveryCompetitor -- <- START HERE

// The bench runs each case against each competitor: without both scores there
// is nothing to compare, which is what it exists for.
func TestRunPassesEveryCaseThroughEveryCompetitor(t *testing.T) {
	roca := &testRoca{byMethod: map[string]bench.Observed{
		search.MethodLike: {Texts: []string{"sentinel"}, Rows: 1, LatencyMS: 3000},
		search.MethodFTS:  {Texts: []string{"sentinel"}, Rows: 1, LatencyMS: 10},
	}}

	res, err := bench.Run(context.Background(), benchOfTwo(), "bench.yaml", roca, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if roca.calls != 4 {
		t.Errorf("calls = %d, want 2 cases times 2 competitors", roca.calls)
	}
	if len(res.Scores) != 2 {
		t.Fatalf("scores = %d, want one per competitor", len(res.Scores))
	}
	for _, score := range res.Scores {
		if score.Passed != 2 || score.Percent != 100 {
			t.Errorf("score of %s = %d/%d (%.0f%%), want 2/2",
				score.Method, score.Passed, score.Total, score.Percent)
		}
	}
}

// -/ 2/4

// -- 3/4 HELPER · failure and degradation contracts --

// A case that blows up counts as its own failure and the run carries on. A bench
// that stops at the first red is no use for knowing how many reds there are.
func TestACaseThatBlowsUpDoesNotStopTheRun(t *testing.T) {
	roca := &testRoca{failure: fmt.Errorf("the database is closed")}

	res, err := bench.Run(context.Background(), benchOfTwo(), "bench.yaml", roca,
		[]string{search.MethodFTS})
	if err != nil {
		t.Fatalf("Run returned an error instead of recording it: %v", err)
	}
	if len(res.Verdicts) != 2 {
		t.Fatalf("verdicts = %d, want one per case even when they all fail", len(res.Verdicts))
	}
	for _, v := range res.Verdicts {
		if v.Passed {
			t.Errorf("the case %q passed with a broken database", v.Case)
		}
		if len(v.Failures) == 0 || !strings.Contains(v.Failures[0], "is closed") {
			t.Errorf("the failure of case %q does not say what happened: %v", v.Case, v.Failures)
		}
	}
}

// An "fts" score that actually ran over LIKE is a false score: it happens when
// the database is not indexed yet. The runner counts the degradations so they
// can be seen.
func TestRunCountsTheDegradations(t *testing.T) {
	roca := &testRoca{byMethod: map[string]bench.Observed{
		search.MethodFTS: {Texts: []string{"sentinel"}, Rows: 1, Method: search.MethodLike},
	}}
	res, err := bench.Run(context.Background(), benchOfTwo(), "bench.yaml", roca,
		[]string{search.MethodFTS})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Scores[0].Degraded != 2 {
		t.Errorf("degraded = %d, want 2: the engine did not run the requested method",
			res.Scores[0].Degraded)
	}
}

// -/ 3/4

// -- 4/4 HELPER · TestWriteEmitsTheCompactTable --

func TestWriteEmitsTheCompactTable(t *testing.T) {
	roca := &testRoca{byMethod: map[string]bench.Observed{
		search.MethodLike: {Texts: []string{"sentinel"}, Rows: 1, LatencyMS: 3000},
		search.MethodFTS:  {Rows: 0, LatencyMS: 10},
	}}
	res, _ := bench.Run(context.Background(), benchOfTwo(), "doradas.yaml", roca,
		[]string{search.MethodLike, search.MethodFTS})

	var out bytes.Buffer
	bench.Write(&out, res)
	text := out.String()

	for _, expected := range []string{
		"bench: doradas.yaml", "cases: 2",
		"scores[2]{method,passed,score,p50,p95}:",
		"like", "3.0 s", "fts", "10 ms", "failures[",
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("the output does not carry %q:\n%s", expected, text)
		}
	}
}

// -/ 4/4
