package service_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// indexFingerprint is what a rebuild of the full-text index changes: the
// tables are dropped and recreated, and their markers rewritten.
func indexFingerprint(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var fingerprint string
	if err := db.QueryRow(`SELECT
		(SELECT group_concat(name || ':' || rootpage) FROM sqlite_master WHERE name LIKE '%_fts%') || '|' ||
		(SELECT group_concat(key || '=' || value || '@' || updated_at) FROM search_state)`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	return fingerprint
}

// initializedWith is an initialized installation holding the seeded rows, and
// the fingerprint of its index after the triggers indexed them.
func initializedWith(t *testing.T, seed string) (testPaths, string) {
	t.Helper()
	paths := freshPaths(t)
	if _, err := serviceOn(t, paths).Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", paths.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	return paths, indexFingerprint(t, paths.db)
}

// initAgain runs init over an existing installation and says whether it took
// the rebuild path.
// The probe's own duration is read off the progress stream: from the line that
// announces it to the line that reports it.
func initAgain(t *testing.T, paths testPaths, timeout time.Duration) (service.InitResult, bool, time.Duration) {
	t.Helper()
	var progress []string
	var asked, answered time.Time
	svc := serviceOn(t, paths, func(o *service.Options) {
		o.QueryTimeout = timeout
		o.Progress = func(line string) {
			if strings.HasPrefix(line, "word search: asking the index") {
				asked = time.Now()
			} else if !asked.IsZero() && answered.IsZero() {
				answered = time.Now()
			}
			progress = append(progress, line)
		}
	})
	result, err := svc.Init(t.Context())
	if err != nil {
		t.Fatalf("init: %v (progress: %v)", err, progress)
	}
	return result, strings.Contains(strings.Join(progress, "\n"), "rebuilding the full-text index"),
		answered.Sub(asked)
}

// On a populated, healthy corpus the probe answers in under a second and init
// leaves the index exactly as it found it. The newest exchanges carry no human
// text (tool-only turns), so the probe has to page back to find a word: a probe
// that sorts the whole table pays that scan once per page. Ids that sort late
// as text too keep a text-ordered probe from finding a word early.
func TestInitOnAHealthyLargeCorpusProvesWordSearchWithoutRebuilding(t *testing.T) {
	paths, before := initializedWith(t, `INSERT INTO sessions (session_id, title) VALUES ('s', 'harbour');
		WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n WHERE x < 200000)
		INSERT INTO exchanges (session_id, exchange_number, human_text, agent_text)
		SELECT 's', x, CASE WHEN x BETWEEN 100000 AND 149999 THEN 'lighthouse keeper' END, 'answered ' || x FROM n`)

	result, rebuilt, probe := initAgain(t, paths, time.Second)
	if probe >= time.Second {
		t.Fatalf("the word-search probe over 200k exchanges took %s", probe)
	}
	if rebuilt {
		t.Fatalf("init rebuilt a healthy index: %+v", result.WordSearch)
	}
	if result.WordSearch == nil || !result.WordSearch.Ready {
		t.Fatalf("word search on 200k exchanges did not prove ready within a second: %+v", result.WordSearch)
	}
	if after := indexFingerprint(t, paths.db); after != before {
		t.Fatalf("init touched a healthy index:\nbefore %s\nafter  %s", before, after)
	}
}

// A probe that runs out of time says so, and a slow answer is not evidence
// that a built index is broken.
func TestATimedOutProbeIsReportedAndDoesNotRebuild(t *testing.T) {
	paths, before := initializedWith(t,
		`INSERT INTO memories (layer, content, origin) VALUES ('fact', 'harbour lighthouse', 'agent')`)

	var progress []string
	svc := serviceOn(t, paths, func(o *service.Options) {
		o.QueryTimeout = time.Nanosecond
		o.Progress = func(line string) { progress = append(progress, line) }
	})
	result, err := svc.Init(t.Context())
	if err == nil {
		t.Fatalf("init succeeded without a word-search proof: %+v", result.WordSearch)
	}
	if !strings.Contains(err.Error(), "word search could not be proven in time") ||
		!strings.Contains(err.Error(), "index was left as it was") ||
		!strings.Contains(err.Error(), "retry with `roca init`") {
		t.Fatalf("the timeout error did not explain the failure and retry: %v", err)
	}
	if result.WordSearch == nil || result.WordSearch.Ready || !result.WordSearch.TimedOut ||
		!strings.Contains(result.WordSearch.Reason, "timed out") {
		t.Fatalf("the timed-out probe was not reported as such: %+v", result.WordSearch)
	}
	if strings.Contains(strings.Join(progress, "\n"), "rebuilding the full-text index") {
		t.Fatalf("a timed-out probe rebuilt the index: %+v", result.WordSearch)
	}
	if after := indexFingerprint(t, paths.db); after != before {
		t.Fatalf("a timed-out probe touched the index:\nbefore %s\nafter  %s", before, after)
	}
}
