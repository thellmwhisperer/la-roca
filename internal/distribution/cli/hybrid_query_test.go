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

func TestQueryFTSOnlyReturnsLabeledHits(t *testing.T) {
	fixtureInstallation(t)
	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "a private note about huerto urbano in gardening", "--origin", "agent")

	out := runRoot(t, contractBuild(), "query", "--databases", "all", "huerto urbano")
	if !strings.Contains(out, "search fts") || !strings.Contains(out, "engines fts") {
		t.Fatalf("FTS-only query lost its engine label:\n%s", out)
	}
	if !strings.Contains(out, "private note about huerto urbano") {
		t.Fatalf("FTS-only query missed the stored note:\n%s", out)
	}
	doc := mustJSON(t, runRoot(t, contractBuild(), "query", "--databases", "all", "huerto urbano", "--json"))
	if fmt.Sprint(doc["row_count"]) == "0" {
		t.Fatalf("json row_count = %v\n%s", doc["row_count"], out)
	}
}
