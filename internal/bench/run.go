package bench

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/human"
	"github.com/thellmwhisperer/la-roca/internal/search"
)

// Competitors are the search methods the bench compares, in order from
// least to most machinery.
//
// The LIKE comes first, and not out of nostalgia: it is the reference the index
// is measured against to see whether it improves anything. A bench without a
// reference gives a number that says nothing, because there is nothing to
// compare it with.
var Competitors = []string{search.MethodLike, search.MethodFTS}

// Querier is what the bench needs from La Roca: asking a question by a specific
// method. It is an interface so the runner can be tested without a database.
type Querier interface {
	Ask(ctx context.Context, question, method string) (Observed, error)
}

// Verdict is how a case fared with one competitor.
type Verdict struct {
	Case      string   `json:"case" yaml:"case"`
	Method    string   `json:"method" yaml:"method"`
	Passed    bool     `json:"passed" yaml:"passed"`
	Failures  []string `json:"failures,omitempty" yaml:"failures,omitempty"`
	LatencyMS int64    `json:"latency_ms" yaml:"latency_ms"`
	Rows      int      `json:"rows" yaml:"rows"`
	// ActualMethod is the one that really ran. When the database is not indexed
	// yet the engine degrades, and an "fts" score that actually ran over LIKE
	// would be a false score.
	ActualMethod string `json:"actual_method,omitempty" yaml:"actual_method,omitempty"`
}

// Score is one competitor's summary over the whole bench.
type Score struct {
	Method   string  `json:"method" yaml:"method"`
	Passed   int     `json:"passed" yaml:"passed"`
	Total    int     `json:"total" yaml:"total"`
	Percent  float64 `json:"score" yaml:"score"`
	P50MS    int64   `json:"p50_ms" yaml:"p50_ms"`
	P95MS    int64   `json:"p95_ms" yaml:"p95_ms"`
	Degraded int     `json:"degraded,omitempty" yaml:"degraded,omitempty"`
}

// Result is the bench's complete run.
type Result struct {
	File     string    `json:"file" yaml:"file"`
	Cases    int       `json:"cases" yaml:"cases"`
	Scores   []Score   `json:"scores" yaml:"scores"`
	Verdicts []Verdict `json:"verdicts" yaml:"verdicts"`
}

// Run passes the whole bench through each competitor.
//
// No case is left unrun and none drags another down: a case that blows up counts
// as its own failure with its own reason, and the run carries on. A bench that
// stops at the first red is no use for what it exists for, which is knowing how
// many there are.
func Run(ctx context.Context, goldenBench Bench, file string, roca Querier,
	methods []string) (Result, error) {

	if len(methods) == 0 {
		methods = Competitors
	}
	res := Result{File: file, Cases: len(goldenBench.Cases)}

	for _, method := range methods {
		latencies := make([]int64, 0, len(goldenBench.Cases))
		score := Score{Method: method, Total: len(goldenBench.Cases)}

		for _, benchCase := range goldenBench.Cases {
			start := time.Now()
			obs, err := roca.Ask(ctx, benchCase.Question, method)
			if obs.LatencyMS == 0 {
				obs.LatencyMS = time.Since(start).Milliseconds()
			}
			if err != nil {
				obs.Error = err.Error()
			}

			failures := benchCase.Judge(obs)
			verdict := Verdict{
				Case: benchCase.ID, Method: method, Passed: len(failures) == 0, Failures: failures,
				LatencyMS: obs.LatencyMS, Rows: obs.Rows, ActualMethod: obs.Method,
			}
			if verdict.Passed {
				score.Passed++
			}
			if obs.Method != "" && obs.Method != method {
				score.Degraded++
			}
			latencies = append(latencies, obs.LatencyMS)
			res.Verdicts = append(res.Verdicts, verdict)
		}

		if score.Total > 0 {
			score.Percent = float64(score.Passed) * 100 / float64(score.Total)
		}
		score.P50MS, score.P95MS = percentiles(latencies)
		res.Scores = append(res.Scores, score)
	}
	return res, nil
}

// percentiles returns the median and the 95th percentile by the nearest-rank
// method, which is the honest one with samples of 25 and not of 25,000.
func percentiles(values []int64) (p50, p95 int64) {
	if len(values) == 0 {
		return 0, 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[len(sorted)*50/100], sorted[min(len(sorted)*95/100, len(sorted)-1)]
}

// Write prints the result in the house's compact shape: one key per line, and
// the tables declaring their columns in the header.
func Write(w io.Writer, res Result) {
	fmt.Fprintf(w, "bench: %s\n", res.File)
	fmt.Fprintf(w, "cases: %d\n", res.Cases)

	fmt.Fprintf(w, "scores[%d]{method,passed,score,p50,p95}:\n", len(res.Scores))
	for _, score := range res.Scores {
		line := fmt.Sprintf("  %-8s %3d/%-3d %5.0f%% %7s %7s",
			score.Method, score.Passed, score.Total, score.Percent,
			human.Duration(score.P50MS), human.Duration(score.P95MS))
		if score.Degraded > 0 {
			line += fmt.Sprintf("  (degraded: %d)", score.Degraded)
		}
		fmt.Fprintln(w, line)
	}

	var failed []Verdict
	for _, v := range res.Verdicts {
		if !v.Passed {
			failed = append(failed, v)
		}
	}
	if len(failed) == 0 {
		fmt.Fprintln(w, "failures[0]: none")
		return
	}
	fmt.Fprintf(w, "failures[%d]{method,case,why}:\n", len(failed))
	for _, v := range failed {
		fmt.Fprintf(w, "  %-8s %-28s %s\n", v.Method, v.Case, strings.Join(v.Failures, "; "))
	}
}
