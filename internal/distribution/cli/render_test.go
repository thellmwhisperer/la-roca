package cli

import (
	"io"
	"strings"
	"testing"
)

// The readable rendering has to work for all 33 templates, not only for term
// search. The previous wave painted the rows by reading the "source" and "text"
// columns by hand, so everything else came out as "[<nil>] <nil>": a count, a
// session and a grouping do not have those columns.
//
// One case per row-shape family, which are the ones the templates emit.

func TestACountPrintsTheNumberAndNothingElse(t *testing.T) {
	output := rowOutput([]string{"total"}, []map[string]any{{"total": int64(1908)}})
	if output != "1908" {
		t.Errorf("output = %q, want %q: a count is its number", output, "1908")
	}
}

func TestASessionShowsItsProjectAndItsDate(t *testing.T) {
	columns := []string{"session_id", "source_agent", "project", "started_at",
		"ended_at", "duration_minutes", "title", "metadata"}
	rows := []map[string]any{{
		"session_id":       "8f21",
		"source_agent":     "claude-code",
		"project":          "nortada",
		"started_at":       "2026-08-04 21:45:10",
		"ended_at":         nil,
		"duration_minutes": int64(37),
		"title":            nil,
		"metadata":         "{}",
	}}
	output := rowOutput(columns, rows)
	for _, chunk := range []string{"project,started_at", "nortada,\"2026-08-04 21:45:10\""} {
		if !strings.Contains(output, chunk) {
			t.Errorf("the session does not show %q:\n%s", chunk, output)
		}
	}
	// A tabular row keeps its declared width; absent values are explicit nulls.
	if !strings.Contains(output, "ended_at") || !strings.Contains(output, "title") ||
		!strings.Contains(output, ",null,") {
		t.Errorf("the table does not preserve nullable columns:\n%s", output)
	}
}

func TestAGroupedResultUsesExactTOON(t *testing.T) {
	got := rowOutput([]string{"project", "count"}, []map[string]any{
		{"project": "nortada", "count": int64(107)},
		{"project": "(none)", "count": int64(12)},
	})
	want := "rows[2]{project,count}:\n" +
		"  nortada,107\n" +
		"  (none),12"
	if got != want {
		t.Fatalf("TOON grouping differs:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestSearchUsesTheExactAXITOONShape(t *testing.T) {
	got := rowOutput([]string{"source", "id", "text", "created_at"}, []map[string]any{
		{"source": "memory", "id": int64(11), "text": "the team, hates the long dashes",
			"created_at": "2026-08-04 21:45:10"},
		{"source": "thinking", "id": int64(90), "text": "some reasoning", "created_at": nil},
	})
	want := "rows[2]{source,id,created_at,text}:\n" +
		"  memory,11,\"2026-08-04 21:45:10\",\"the team, hates the long dashes\"\n" +
		"  thinking,90,null,some reasoning"
	if got != want {
		t.Fatalf("TOON search differs:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTOONQuotesStructuralColumnNamesAndNumericStrings(t *testing.T) {
	got := rowOutput([]string{"COUNT(*)", "value"}, []map[string]any{
		{"COUNT(*)": int64(1), "value": "1e999"},
		{"COUNT(*)": int64(2), "value": "plain"},
	})
	want := "rows[2]{\"COUNT(*)\",value}:\n  1,\"1e999\"\n  2,plain"
	if got != want {
		t.Fatalf("TOON quoting differs:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The column order is the query's, not that of a map's iteration, which in Go is
// deliberately random: without this, two runs of the same query print the same
// row in two different ways.
func TestTheColumnOrderIsTheQueryOrder(t *testing.T) {
	columns := []string{"id", "layer", "content", "created_at"}
	row := map[string]any{"id": int64(3), "layer": "feedback",
		"content": "a note", "created_at": "2026-08-05"}

	first := rowOutput(columns, []map[string]any{row})
	for range 20 {
		if other := rowOutput(columns, []map[string]any{row}); other != first {
			t.Fatalf("two renderings of the same row differ:\n%s\n%s", first, other)
		}
	}
	if first != "rows[1]{id,layer,content,created_at}:\n  3,feedback,a note,2026-08-05" {
		t.Errorf("the columns do not come out in the query's order:\n%s", first)
	}
}

func TestALongTextIsFlattenedAndClipped(t *testing.T) {
	length := "first line\nsecond line " + strings.Repeat("filler ", 60)
	output := rowOutput([]string{"content"}, []map[string]any{{"content": length}})
	if strings.Contains(output, "\n") {
		t.Errorf("the text still has line breaks:\n%s", output)
	}
	if len([]rune(output)) > fieldWidth {
		t.Errorf("the field takes %d characters", len([]rune(output)))
	}
	if !strings.Contains(output, "first line second line") {
		t.Errorf("the clipping ate the beginning:\n%s", output)
	}
}

func TestAnAverageIsNotPrintedWithTwentyDecimals(t *testing.T) {
	output := rowOutput([]string{"average"}, []map[string]any{{"average": 3.4666666666666663}})
	if output != "3.46667" {
		t.Errorf("average = %q, want 3.46667", output)
	}
}

func TestWithoutRowsThereAreNoLines(t *testing.T) {
	if output := rowOutput([]string{"total"}, nil); output != "" {
		t.Errorf("output = %q, want none", output)
	}
}

// A column the query did not declare is not lost: the rendering cannot hide a
// piece of data because the caller left the column list behind.
func TestAnUndeclaredColumnStillShowsUp(t *testing.T) {
	output := rowOutput([]string{"id"}, []map[string]any{
		{"id": int64(1), "surprise": "here I am"},
	})
	if output != "rows[1]{id,surprise}:\n  1,here I am" {
		t.Errorf("the undeclared column was lost:\n%s", output)
	}
}

func TestRuntimeListingsUseTOONRows(t *testing.T) {
	type report struct{ runtime, state, detail string }
	var output strings.Builder
	err := runtimeStatus(&cliEnv{out: &output}, nil, []string{"codex", "claude"},
		func(runtime string) (report, error) {
			return report{runtime: runtime, state: "configured", detail: runtime + ".json"}, nil
		}, []string{"runtime", "state", "detail"}, func(r report) map[string]any {
			return map[string]any{"runtime": r.runtime, "state": r.state, "detail": r.detail}
		})
	if err != nil {
		t.Fatal(err)
	}
	want := "rows[2]{runtime,state,detail}:\n" +
		"  codex,configured,codex.json\n" +
		"  claude,configured,claude.json\n"
	if output.String() != want {
		t.Fatalf("runtime TOON differs:\n--- got ---\n%s--- want ---\n%s", output.String(), want)
	}
}

// `roca --version` is the second line of the reference flow (PRD R1) and
// the health check `install.sh` and `roca update` run on a binary before they
// trust it. It has to exist, and it has to answer the same thing the subcommand
// answers: two spellings of the same question with two different answers is two
// products.
func TestTheVersionFlagAnswersWhatTheSubcommandAnswers(t *testing.T) {
	build := Build{Version: "v1.2.3", Commit: "0123abc", Date: "2026-08-05T00:00:00Z"}

	flagged := runRoot(t, build, "--version")
	subcommand := runRoot(t, build, "version")

	if flagged != subcommand {
		t.Fatalf("--version says %q and `version` says %q", flagged, subcommand)
	}
	for _, wanted := range []string{"roca", "v1.2.3", "0123abc"} {
		if !strings.Contains(flagged, wanted) {
			t.Errorf("the version line %q does not carry %q", flagged, wanted)
		}
	}
	if !strings.HasPrefix(flagged, "roca ") {
		t.Errorf("the version line %q does not start with the product's name: "+
			"install.sh reads it to tell a roca binary from somebody else's file", flagged)
	}
}

func runRoot(t *testing.T, build Build, args ...string) string {
	t.Helper()
	out, err := runRootErr(t, build, nil, args...)
	if err != nil {
		t.Fatalf("roca %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// runRootErr is the same surface for tests that need stdin or want the error.
func runRootErr(t *testing.T, build Build, in io.Reader, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	env := &cliEnv{build: build, out: &out, errOut: &out}
	root := rootCommand(env)
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&out)
	if in != nil {
		root.SetIn(in)
	}
	err := root.Execute()
	return out.String(), err
}
