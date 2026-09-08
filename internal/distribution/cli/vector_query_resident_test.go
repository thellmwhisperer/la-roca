package cli

import "testing"

func TestParseVectorQueryInvocation(t *testing.T) {
	inv, ok := parseVectorQueryInvocation([]string{
		"--json", "query", "--expand-templates", "--min-score", "0.35",
		"--databases", "ops", "harbor lantern", "3",
	})
	if !ok {
		t.Fatal("query invocation was not recognized")
	}
	if inv.query != "harbor lantern" || inv.k != 3 || inv.databases != "ops" ||
		!inv.json || !inv.expandTemplates || inv.minScore != 0.35 {
		t.Fatalf("parsed invocation = %+v", inv)
	}
	if _, ok := parseVectorQueryInvocation([]string{"status"}); ok {
		t.Fatal("status was parsed as a query")
	}
}
