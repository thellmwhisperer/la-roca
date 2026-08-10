package bench_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/bench"
)

func writeBench(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bench.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the bench: %v", err)
	}
	return path
}

const minimalBench = `
version: 1
generator: prueba
cases:
  - id: termino-simple
    question: que sabemos de los guiones largos
    expect_path: compiler
    expect_template: search_all_sources_by_term
    expect_rows_contain: ["guiones largos"]
    max_latency_ms: 500
`

func TestLoadReadsTheBenchAndItsCases(t *testing.T) {
	goldenBench, err := bench.Load(writeBench(t, minimalBench))
	if err != nil {
		t.Fatalf("Cargar: %v", err)
	}
	if goldenBench.Version != 1 {
		t.Errorf("Version = %d, want 1", goldenBench.Version)
	}
	if len(goldenBench.Cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(goldenBench.Cases))
	}
	benchCase := goldenBench.Cases[0]
	if benchCase.ID != "termino-simple" {
		t.Errorf("ID = %q", benchCase.ID)
	}
	if benchCase.ExpectTemplate != "search_all_sources_by_term" {
		t.Errorf("ExpectTemplate = %q", benchCase.ExpectTemplate)
	}
	if len(benchCase.ExpectRowsContain) != 1 {
		t.Errorf("ExpectRowsContain = %v", benchCase.ExpectRowsContain)
	}
}

// A broken bench is called out at load time and not halfway through the run:
// finding out at case 19 of 25 that the file is wrong is finding out late.
func TestLoadRejectsABenchThatDoesNotHoldUp(t *testing.T) {
	benchCases := map[string]string{
		"no version":              "cases:\n  - id: a\n    question: b\n",
		"version from the future": "version: 99\ncases:\n  - id: a\n    question: b\n",
		"no cases":                "version: 1\ncases: []\n",
		"case with no id":         "version: 1\ncases:\n  - question: b\n",
		"case with no question":   "version: 1\ncases:\n  - id: a\n",
		"repeated identifier":     "version: 1\ncases:\n  - id: a\n    question: b\n  - id: a\n    question: c\n",
		"invented path":           "version: 1\ncases:\n  - id: a\n    question: b\n    expect_path: telepatia\n",
	}
	for name, content := range benchCases {
		t.Run(name, func(t *testing.T) {
			if _, err := bench.Load(writeBench(t, content)); err == nil {
				t.Error("Load accepted a bench that does not hold up")
			}
		})
	}
}

func TestLoadNamesTheMissingFile(t *testing.T) {
	_, err := bench.Load(filepath.Join(t.TempDir(), "no-existe.yaml"))
	if err == nil {
		t.Fatal("Load accepted a file that does not exist")
	}
	if !strings.Contains(err.Error(), "no-existe.yaml") {
		t.Errorf("the error does not name the file: %v", err)
	}
}

// --- a case's verdict ---

func TestACasePassesWhenEverythingItDeclaresHolds(t *testing.T) {
	benchCase := bench.Case{
		ID:                "x",
		Question:          "da igual",
		ExpectPath:        "compiler",
		ExpectTemplate:    "search_all_sources_by_term",
		ExpectRowsContain: []string{"guiones largos"},
		ExpectMinRows:     1,
		MaxLatencyMS:      500,
	}
	obs := bench.Observed{
		Path: "compiler", Template: "search_all_sources_by_term",
		Texts: []string{"aquí se habla de GUIONES LARGOS y de más cosas"},
		Rows:  1, LatencyMS: 12,
	}
	if failures := benchCase.Judge(obs); len(failures) != 0 {
		t.Errorf("the case failed for no reason: %v", failures)
	}
}

func TestACaseFailsAndSaysExactlyWhy(t *testing.T) {
	benchCase := bench.Case{
		ID: "x", Question: "da igual",
		ExpectPath:        "compiler",
		ExpectRowsContain: []string{"sentinel"},
		ExpectMinRows:     3,
		MaxLatencyMS:      100,
	}
	obs := bench.Observed{Path: "unresolved", Texts: []string{"otra cosa"}, Rows: 1, LatencyMS: 900}

	failures := benchCase.Judge(obs)
	if len(failures) != 4 {
		t.Fatalf("failures = %v, want one per unmet criterion", failures)
	}
	alongside := strings.Join(failures, " | ")
	for _, expected := range []string{"path", "sentinel", "rows", "latency"} {
		if !strings.Contains(alongside, expected) {
			t.Errorf("the failures do not name %q: %v", expected, failures)
		}
	}
}

// The sentinel is searched for ignoring case and diacritics, which is how a
// person reading the output would look for it.
func TestTheSentinelIsSearchedFolded(t *testing.T) {
	benchCase := bench.Case{ID: "x", Question: "q", ExpectRowsContain: []string{"guiones largos"}}
	obs := bench.Observed{Texts: []string{"GUIONES LARGOS"}, Rows: 1}
	if failures := benchCase.Judge(obs); len(failures) != 0 {
		t.Errorf("the sentinel did not match folded: %v", failures)
	}
}

// Honest zero rows: a case can declare that the correct answer is that there is
// nothing, and then bringing rows is the failure.
func TestACaseCanRequireThatThereIsNothing(t *testing.T) {
	benchCase := bench.Case{ID: "x", Question: "q", ExpectEmpty: true}
	if failures := benchCase.Judge(bench.Observed{Rows: 0}); len(failures) != 0 {
		t.Errorf("requiring empty failed with zero rows: %v", failures)
	}
	if failures := benchCase.Judge(bench.Observed{Rows: 2}); len(failures) == 0 {
		t.Error("requiring empty passed with two rows")
	}
}

func TestACaseCanRequireARefusalWithAReason(t *testing.T) {
	benchCase := bench.Case{ID: "x", Question: "q", ExpectPath: "refused", ExpectRefusal: "out_of_scope"}
	if failures := benchCase.Judge(bench.Observed{Path: "refused", Refusal: "out_of_scope"}); len(failures) != 0 {
		t.Errorf("the expected refusal failed: %v", failures)
	}
	failures := benchCase.Judge(bench.Observed{Path: "refused", Refusal: "ambiguous"})
	if len(failures) != 1 || !strings.Contains(failures[0], "refusal reason") {
		t.Errorf("a different reason was not detected: %v", failures)
	}
}

// The contrast seed has to load and hold up. It is a test fixture and not a
// product artefact: the binary carries no questions inside it, and this test is
// the guard that it stays that way.
func TestTheContrastSeedHoldsUp(t *testing.T) {
	goldenBench, err := bench.Load(filepath.Join("testdata", "seed.yaml"))
	if err != nil {
		t.Fatalf("Load the seed: %v", err)
	}
	if len(goldenBench.Cases) < 15 {
		t.Errorf("the seed carries %d cases; the PRD asks for between 15 and 25", len(goldenBench.Cases))
	}
	if len(goldenBench.Cases) > 25 {
		t.Errorf("the seed carries %d cases; the PRD asks for between 15 and 25", len(goldenBench.Cases))
	}

	// At least one case has to measure real recall: without sentinels, the
	// bench would compare three routes that all give the same aggregate and
	// would tell none of them apart.
	withSentinel := 0
	for _, benchCase := range goldenBench.Cases {
		if len(benchCase.ExpectRowsContain) > 0 {
			withSentinel++
		}
	}
	if withSentinel < 3 {
		t.Errorf("only %d cases carry a sentinel: the bench would not separate the competitors",
			withSentinel)
	}
}
