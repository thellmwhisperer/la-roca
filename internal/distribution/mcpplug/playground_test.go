package mcpplug_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPlaygroundQuestionsRemainPositionalArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	executable := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$HOME/argv\"\nprintf '%s\\n' '{}'\nprintf '%s\\n' '{\"stderr\":\"\",\"query\":{}}' >&2\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	svc := readOnlyService(t)
	session := connect(t, svc)
	for _, tool := range []string{"roca_sql", "roca_explore"} {
		for _, question := range []string{"--", "--read-only=false", "--db-path=other.db", "a normal question"} {
			t.Run(tool+"/"+question, func(t *testing.T) {
				input := map[string]any{"query": question, "layer": "project", "databases": "corpus"}
				verb := "playground"
				if tool == "roca_explore" {
					verb = "explore"
					input["max_chars"], input["deep"] = 900, true
				}
				response := callTool(t, session, tool, input)
				if response.IsError {
					t.Fatalf("plugin call failed: %s", renderedText(response))
				}
				raw, err := os.ReadFile(filepath.Join(home, "argv"))
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"--transport", "--json", verb, "--db-path", svc.DB().Path()}
				if tool == "roca_sql" {
					want = append(want, "--sql-only")
				}
				want = append(want, "--layer", "project", "--databases", "corpus")
				if tool == "roca_explore" {
					want = append(want, "--max-chars", "900", "--deep")
				}
				want = append(want, "--read-only", "--", question)
				if got := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
					t.Fatalf("plugin arguments = %q, want %q", got, want)
				}
			})
		}
	}
}

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

func TestPlaygroundValidationErrorsPreserveDiagnosticsAndScrubDatabasePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	executable := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	svc := readOnlyService(t)
	diagnostic := "unknown database missing in " + svc.DB().Path() + "\n"
	transport, err := json.Marshal(map[string]string{"stderr": diagnostic})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		stderr string
	}{
		{"transport", string(transport)},
		{"plain", diagnostic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := "#!/bin/sh\ncat <<'DIAGNOSTIC' >&2\n" + tc.stderr + "\nDIAGNOSTIC\nexit 1\n"
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			session := connect(t, svc)
			for _, tool := range []string{"roca_sql", "roca_explore"} {
				response := callToolResult(t, session, tool, map[string]any{"query": "synthetic question", "databases": "missing"})
				output := renderedText(response)
				if !response.IsError || !strings.Contains(output, "unknown database missing in the database") {
					t.Errorf("%s lost validation diagnostic: %s", tool, output)
				}
				if strings.Contains(output, svc.DB().Path()) {
					t.Errorf("%s exposed the database path: %s", tool, output)
				}
			}
		})
	}
}
