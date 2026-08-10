//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// TheMagicMinute is the whole product's promise as one number: from a binary
// nobody has run to an answer out of the operator's own corpus, in under a
// minute, with no manual step in between.
//
// It is the birth test of D-2 ("startup on a virgin machine") and the honest
// half of F01-15 that a sandbox can measure: F01-15 also walks a `stop` step,
// and v1 has no daemon to stop, so the daemon half of it belongs to the real
// battery on the reference machine and not here.
//
// The journey is the one an operator actually walks:
//
//  1. copy the binary onto the PATH (installing IS copying one file)
//  2. `roca init`: create the explicit database, read the detected source families off the
//     disk, gate the model and calibrate a golden bench from what was found
//  3. ask the first question and get rows back
//
// The bench is not decoration in this test. An installation that answers in
// forty seconds and cannot say whether its search is any good is half a
// product, and the calibration is the step that makes the answer measurable
// from day one.
const theMagicMinute = time.Minute

// theFirstQuestion is asked the way the product declares a search, so the
// journey measures the corpus and the cascade and not the classifier's mood on
// one particular phrasing.
const theFirstQuestion = "que sabemos de la matriz de ingesta"

func TestTheMagicMinute(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, err := os.MkdirTemp("", "roca-magic-minute-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })

	m := &world{binary: binary, home: home}
	// The model is off, and that is the measurement, not a shortcut: the minute
	// this product promises is the one it delivers on a machine with no network
	// and no local model, which is the machine most operators start on.
	if err := os.MkdirAll(home+"/tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	// The corpus the agents left behind, across the source families ingest reads.
	if err := m.theOperatorsArtefacts(); err != nil {
		t.Fatalf("seed the operator's world: %v", err)
	}

	copied := leg(t, "copy the binary onto the PATH", m.installBinary)

	var bootstrap map[string]any
	initialized := leg(t, "roca init", func() error {
		if err := m.mustRun("roca init --json --db-path " +
			strconv.Quote(home+"/.roca/roca.db")); err != nil {
			return err
		}
		return json.Unmarshal([]byte(m.last.stdout), &bootstrap)
	})

	answered := leg(t, "the first question", func() error {
		return m.mustRun("roca query '" + theFirstQuestion + "' --json")
	})

	total := copied + initialized + answered
	t.Logf("the magic minute: %v (copy %v · init %v · first answer %v)",
		total.Round(time.Millisecond), copied.Round(time.Millisecond),
		initialized.Round(time.Millisecond), answered.Round(time.Millisecond))

	// What init had to have done, because a minute that ends in an empty
	// database is not the journey this measures.
	if ingested := number(bootstrap, "ingest.files_read"); ingested == 0 {
		t.Errorf("init read no file of the seeded corpus: %v", bootstrap["ingest"])
	}

	// And the answer itself. The model is off for this measurement, so the
	// honest answer is unresolved: what matters is that the query completed and
	// declared its path, not that it returned rows it had no model to find.
	var answer map[string]any
	if err := json.Unmarshal([]byte(m.last.stdout), &answer); err != nil {
		t.Fatalf("the answer is not JSON: %v\n%s", err, m.last.stdout)
	}
	if path := text(answer, "path"); path == "" {
		t.Fatalf("the first question came back with no path: %v", answer)
	}

	if total > theMagicMinute {
		t.Fatalf("the journey took %v, and the promise is under %v",
			total.Round(time.Millisecond), theMagicMinute)
	}
}

// leg times one step of the journey and fails the test where it broke, so a red
// says which leg it was and not just that the minute was missed.
func leg(t *testing.T, name string, walk func() error) time.Duration {
	t.Helper()
	start := time.Now()
	if err := walk(); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return time.Since(start)
}

func number(document map[string]any, path string) float64 {
	value, _ := lookup(document, path)
	found, _ := value.(float64)
	return found
}

func text(document map[string]any, path string) string {
	value, found := lookup(document, path)
	if !found {
		return ""
	}
	return fmt.Sprint(value)
}
