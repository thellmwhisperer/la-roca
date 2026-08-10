// Package bench is the golden query bench: the file format and the runner that
// runs it.
//
// There is not a single question written here. The binary carries the format and
// the runner, never a fixed exam: each installation generates the bench from ITS
// OWN corpus (the addendum of 2026-08-05, "nothing of mine has to
// travel in anybody else's binary"). A bench is a versioned data file the
// operator keeps on their disk and hands to the command.
//
// What it is for: turning "search got better" into a number. The same bench is
// run against the competitors (the reference LIKE and the lexical index) and
// each one's score is published. Without that, changing search method is a bet.
package bench

import (
	"fmt"
	"os"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/search"
	"gopkg.in/yaml.v3"

	"github.com/thellmwhisperer/la-roca/internal/human"
)

// SupportedVersion is that of the format this binary understands. A bench from a
// later version is rejected instead of interpreted halfway: a score drawn from
// criteria the binary did not understand is not comparable with any other.
const SupportedVersion = 1

// Bench is a file of golden queries.
type Bench struct {
	Version     int    `yaml:"version"`
	GeneratedAt string `yaml:"generated_at,omitempty"`
	// Generator says who wrote it: the calibration command with its version,
	// or a person. A score you cannot trace to a bench cannot be compared with
	// next month's.
	Generator string `yaml:"generator,omitempty"`
	Notes     string `yaml:"notes,omitempty"`
	Corpus    Corpus `yaml:"corpus,omitempty"`
	Cases     []Case `yaml:"cases"`
}

// Corpus describes what the bench was generated against, so that it shows when
// the bench has gone stale with respect to the memory it measures.
type Corpus struct {
	Memories  int    `yaml:"memories,omitempty"`
	Exchanges int    `yaml:"exchanges,omitempty"`
	DBPath    string `yaml:"-"`
}

// Case is a golden query with its relevance criterion declared.
//
// Every criterion is optional except the identifier and the question: a case
// that declares nothing is run and only checks that it does not blow up, which
// is also a test.
type Case struct {
	ID       string `yaml:"id"`
	Question string `yaml:"question"`

	// ExpectPath is the path the question has to leave by.
	ExpectPath string `yaml:"expect_path,omitempty"`
	// ExpectTemplate is the template that must compile.
	ExpectTemplate string `yaml:"expect_template,omitempty"`
	// ExpectRefusal is the reason for the refusal, when one is expected.
	ExpectRefusal string `yaml:"expect_refusal,omitempty"`

	// ExpectRowsContain are the sentinels: each one has to appear in one of the
	// returned rows. It is the real relevance criterion, because it measures what
	// was found and not where it was looked for.
	ExpectRowsContain []string `yaml:"expect_rows_contain,omitempty"`
	ExpectMinRows     int      `yaml:"expect_min_rows,omitempty"`
	ExpectMaxRows     int      `yaml:"expect_max_rows,omitempty"`
	// ExpectEmpty requires zero rows. A question from a foreign domain has to
	// return nothing, and returning something is the failure.
	ExpectEmpty bool `yaml:"expect_empty,omitempty"`

	MaxLatencyMS int64 `yaml:"max_latency_ms,omitempty"`
	// Source is where the case came from. Nobody judges it: it is there so you
	// know who to ask when a case has been failing for months.
	Source string `yaml:"source,omitempty"`
}

// Observed is what actually happened when the case ran.
type Observed struct {
	Path      string
	Template  string
	Refusal   string
	Texts     []string
	Rows      int
	LatencyMS int64
	Method    string
	Error     string
}

var knownPaths = map[string]bool{
	"compiler": true, "refused": true, "unresolved": true,
	"llm_fallback": true, "keyword_fallback": true,
}

// Load reads a bench and checks that it holds up before returning it.
//
// The validation happens all at once and up front on purpose: finding out at
// case 19 of 25 that the file is wrong is finding out late, with half the run
// spent.
func Load(path string) (Bench, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Bench{}, fmt.Errorf("read the bench %q: %w", path, err)
	}
	var goldenBench Bench
	if err := yaml.Unmarshal(raw, &goldenBench); err != nil {
		return Bench{}, fmt.Errorf("the bench %q is not valid YAML: %w", path, err)
	}
	if err := goldenBench.validate(); err != nil {
		return Bench{}, fmt.Errorf("the bench %q does not hold up: %w", path, err)
	}
	return goldenBench, nil
}

func (b Bench) validate() error {
	if b.Version == 0 {
		return fmt.Errorf("it does not declare `version`")
	}
	if b.Version > SupportedVersion {
		return fmt.Errorf("it is version %d and this binary understands up to %d",
			b.Version, SupportedVersion)
	}
	if len(b.Cases) == 0 {
		return fmt.Errorf("it does not carry a single case")
	}
	seen := map[string]bool{}
	for i, benchCase := range b.Cases {
		if benchCase.ID == "" {
			return fmt.Errorf("case %d has no `id`", i+1)
		}
		if seen[benchCase.ID] {
			return fmt.Errorf("the identifier %q is repeated", benchCase.ID)
		}
		seen[benchCase.ID] = true
		if strings.TrimSpace(benchCase.Question) == "" {
			return fmt.Errorf("case %q has no `question`", benchCase.ID)
		}
		if benchCase.ExpectPath != "" && !knownPaths[benchCase.ExpectPath] {
			return fmt.Errorf("case %q expects path %q, which does not exist",
				benchCase.ID, benchCase.ExpectPath)
		}
	}
	return nil
}

// Judge returns the list of criteria the case declared and that were not met. An
// empty list is a pass. ALL the unmet ones are returned and not just the first:
// fixing a case knowing only the first reason is fixing it blind and running the
// bench again to discover the next one.
func (c Case) Judge(obs Observed) []string {
	var failures []string

	if obs.Error != "" {
		return append(failures, "the query failed: "+obs.Error)
	}
	if c.ExpectPath != "" && obs.Path != c.ExpectPath {
		failures = append(failures, fmt.Sprintf("path %q, expected %q", obs.Path, c.ExpectPath))
	}
	if c.ExpectTemplate != "" && obs.Template != c.ExpectTemplate {
		failures = append(failures, fmt.Sprintf("template %q, expected %q", obs.Template, c.ExpectTemplate))
	}
	if c.ExpectRefusal != "" && obs.Refusal != c.ExpectRefusal {
		failures = append(failures, fmt.Sprintf("refusal reason %q, expected %q",
			obs.Refusal, c.ExpectRefusal))
	}
	for _, sentinel := range c.ExpectRowsContain {
		if !anyTextContains(obs.Texts, sentinel) {
			failures = append(failures, fmt.Sprintf("no row carries the sentinel %q", sentinel))
		}
	}
	if c.ExpectMinRows > 0 && obs.Rows < c.ExpectMinRows {
		failures = append(failures, fmt.Sprintf("%d rows, expected at least %d",
			obs.Rows, c.ExpectMinRows))
	}
	if c.ExpectMaxRows > 0 && obs.Rows > c.ExpectMaxRows {
		failures = append(failures, fmt.Sprintf("%d rows, expected at most %d",
			obs.Rows, c.ExpectMaxRows))
	}
	if c.ExpectEmpty && obs.Rows != 0 {
		failures = append(failures, fmt.Sprintf("%d rows, expected none", obs.Rows))
	}
	if c.MaxLatencyMS > 0 && obs.LatencyMS > c.MaxLatencyMS {
		failures = append(failures, fmt.Sprintf("latency %s, the cap is %s",
			human.Duration(obs.LatencyMS), human.Duration(c.MaxLatencyMS)))
	}
	return failures
}

// anyTextContains looks for the sentinel folding case and diacritics, which is
// how a person skimming the output would look for it.
func anyTextContains(texts []string, sentinel string) bool {
	wanted := strings.Join(search.Tokenize(sentinel), " ")
	if wanted == "" {
		return true
	}
	for _, text := range texts {
		if strings.Contains(strings.Join(search.Tokenize(text), " "), wanted) {
			return true
		}
	}
	return false
}
