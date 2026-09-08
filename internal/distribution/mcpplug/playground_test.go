package mcpplug_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPlaygroundExplorePreservesRequestedTextBudget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	text := strings.Repeat("x", 800)
	result := service.QueryResult{
		Question: "synthetic evidence", Mode: "explore", Path: service.PathLLM,
		Columns: []string{"text"}, Rows: []map[string]any{{"text": text}}, RowCount: 1,
		Degraded: "interpretation_error", CleanedSQL: "SELECT repaired", ModelSQL: "SELECT original",
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ncat <<'RESULT'\n"+string(payload)+"\nRESULT\nprintf '%s\\n' '{\"stderr\":\"\",\"cleaned_sql\":\"SELECT repaired\",\"query\":{}}' >&2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	session := connect(t, seededService(t))
	for _, budget := range []int{0, 900} {
		response := callTool(t, session, "roca_explore", map[string]any{
			"query": result.Question, "max_chars": budget,
		})
		output := renderedText(response)
		if response.Meta["sql"] != "SELECT repaired" || response.Meta["raw_sql"] != "SELECT original" {
			t.Fatalf("audit metadata=%v", response.Meta)
		}
		if response.IsError || !strings.Contains(output, strings.Repeat("x", 450)) {
			t.Fatalf("budget %d lost evidence: %s", budget, output)
		}
		if complete := strings.Contains(output, text); complete != (budget == 900) {
			t.Fatalf("budget %d: complete evidence = %v", budget, complete)
		}
	}
}
