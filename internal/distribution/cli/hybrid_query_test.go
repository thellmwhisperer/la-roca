package cli

import (
	"fmt"
	"strings"
	"testing"
)

func TestPlaygroundHelpTeachesHumanSQLModes(t *testing.T) {
	var output strings.Builder
	root := rootCommand(&cliEnv{})
	root.SetOut(&output)
	root.SetArgs([]string{"playground", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--full", "--sql-only", "humans"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("playground help lacks %q:\n%s", want, output.String())
		}
	}
}

func TestQueryHelpNamesTheHybridKnobs(t *testing.T) {
	var output strings.Builder
	root := rootCommand(&cliEnv{})
	root.SetOut(&output)
	root.SetArgs([]string{"query", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--oversample", "--no-templates", "--top", "--rrf-k",
		"--min-vector-score", "--max-rare-terms", "--parallel-legs"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("query help lacks %q:\n%s", want, output.String())
		}
	}
}

func TestQueryRejectsInvalidNumericKnobsBeforeOpeningTheService(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "oversample below minimum", args: []string{"query", "--oversample", "0", "question"}, want: "oversample must be between 1 and 100"},
		{name: "oversample above maximum", args: []string{"query", "--oversample", "101", "question"}, want: "oversample must be between 1 and 100"},
		{name: "rrf k below minimum", args: []string{"query", "--rrf-k", "0", "question"}, want: "rrf-k must be 1 or greater"},
		{name: "min score non-finite", args: []string{"query", "--min-vector-score", "NaN", "question"}, want: "min-vector-score must be a finite positive number"},
		{name: "rare terms below minimum", args: []string{"query", "--max-rare-terms", "0", "question"}, want: "max-rare-terms must be 1 or greater"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := rootCommand(&cliEnv{})
			root.SetArgs(testCase.args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestDoctorPrintsConfiguredQueryKnobs(t *testing.T) {
	fixture := fixtureInstallation(t)
	writeConfig(t, fixture.home, "[query]\noversample = 30\ntemplates = false\nparallel_legs = true\n")
	out := runRoot(t, contractBuild(), "doctor")
	for _, want := range []string{"oversample 30", "templates false", "parallel_legs true", "rrf_k 60"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor missing %q:\n%s", want, out)
		}
	}
}

func TestQueryFTSOnlyReturnsLabeledHits(t *testing.T) {
	fixtureInstallation(t)
	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "a private note about salud mental in therapy", "--origin", "agent")

	out := runRoot(t, contractBuild(), "query", "salud mental")
	if !strings.Contains(out, "search fts") || !strings.Contains(out, "engines fts") {
		t.Fatalf("default query lost its engine label:\n%s", out)
	}
	if !strings.Contains(out, "private note about salud mental") {
		t.Fatalf("default query missed the stored ops memory:\n%s", out)
	}

	out = runRoot(t, contractBuild(), "query", "--databases", "all", "salud mental")
	if !strings.Contains(out, "search fts") || !strings.Contains(out, "engines fts") {
		t.Fatalf("FTS-only query lost its engine label:\n%s", out)
	}
	if !strings.Contains(out, "private note about salud mental") {
		t.Fatalf("FTS-only query missed the stored note:\n%s", out)
	}
	doc := mustJSON(t, runRoot(t, contractBuild(), "query", "--databases", "all", "salud mental", "--json"))
	if fmt.Sprint(doc["row_count"]) == "0" {
		t.Fatalf("json row_count = %v\n%s", doc["row_count"], out)
	}
}
