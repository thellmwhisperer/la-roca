package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/jsonid"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"modernc.org/sqlite"
)

func TestCrossConcatenationEnvelopeAndValues(t *testing.T) {
	sets := []crossResult{
		{origin: "local", result: service.ExecResult{Columns: []string{"id", "value"}, Rows: []map[string]any{
			{"id": jsonid.Decimal("1152921504606853875"), "value": int64(7)},
			{"id": nil, "value": "7"},
		}}},
		{origin: "empty", result: service.ExecResult{Columns: []string{"missing"}}},
		{origin: "remote", result: service.ExecResult{Rows: []map[string]any{
			{"value": 2.5, "flag": true}, {"flag": false},
		}}},
	}
	before, _ := json.Marshal(sets[0].result)
	result, err := gatherCross(t.Context(), Build{Version: "v-test", Commit: "test-sha"}, sets)
	if err != nil {
		t.Fatal(err)
	}
	want := service.ExecResult{
		SQL:     `SELECT "origin","id","value",NULL AS "missing",NULL AS "flag" FROM "r_local" UNION ALL SELECT "origin",NULL AS "id",NULL AS "value","missing",NULL AS "flag" FROM "r_empty" UNION ALL SELECT "origin",NULL AS "id","value",NULL AS "missing","flag" FROM "r_remote"`,
		Columns: []string{"origin", "id", "value", "missing", "flag"},
		Rows: []map[string]any{
			{"origin": "local", "id": jsonid.Decimal("1152921504606853875"), "value": int64(7), "missing": nil, "flag": nil},
			{"origin": "local", "id": nil, "value": "7", "missing": nil, "flag": nil},
			{"origin": "remote", "id": nil, "value": 2.5, "missing": nil, "flag": true},
			{"origin": "remote", "id": nil, "value": nil, "missing": nil, "flag": false},
		},
		RowCount: 4, MaxChars: service.DefaultMaxChars, Version: "v-test", SourceSHA: "test-sha",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("gather = %#v, want %#v", result, want)
	}
	after, _ := json.Marshal(sets[0].result)
	if string(before) != string(after) {
		t.Fatal("gather mutated its input")
	}
	for _, columns := range [][]string{{"origin"}, {"Origin"}, {"value", "value"}, {"value", "VALUE"}} {
		_, err := gatherCross(t.Context(), Build{}, []crossResult{{origin: "local", result: service.ExecResult{Columns: columns}}})
		if err == nil {
			t.Fatalf("accepted duplicate or reserved columns %v", columns)
		}
	}
	for _, rows := range [][]map[string]any{nil, {{"text": strings.Repeat("界", service.DefaultMaxChars+1)}}} {
		result, err := gatherCross(t.Context(), Build{}, []crossResult{{origin: "local", result: service.ExecResult{
			Columns: []string{"text"}, Rows: rows,
		}}})
		if err != nil {
			t.Fatal(err)
		}
		if result.RowCount != len(rows) || (rows == nil && result.Rows != nil) {
			t.Fatalf("empty envelope changed: %+v", result)
		}
		if len(rows) > 0 && result.Rows[0]["text"] != service.Truncate(rows[0]["text"].(string), service.DefaultMaxChars, "") {
			t.Fatalf("text budget changed: %+v", result)
		}
	}
	if _, err := gatherCross(t.Context(), Build{}, []crossResult{{origin: "local", result: service.ExecResult{
		Columns: []string{"É", "é"},
	}}}); err != nil {
		t.Fatalf("distinct Unicode column names rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := gatherCross(ctx, Build{}, sets); err != context.Canceled {
		t.Fatalf("cancelled gather: %v", err)
	}
}

func TestCostCrossNoSQLiteConnections(t *testing.T) {
	if os.Getenv("ROCA_TEST_CROSS_CONNECTIONS") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestCostCrossNoSQLiteConnections$")
		cmd.Env = append(os.Environ(), "ROCA_TEST_CROSS_CONNECTIONS=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("connection cost check: %v\n%s", err, output)
		}
		return
	}
	var connections atomic.Int64
	sqlite.RegisterConnectionHook(func(sqlite.ExecQuerierContext, string) error {
		connections.Add(1)
		return nil
	})
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := connections.Swap(0); got != 1 {
		t.Fatalf("connection observer counted %d opens, want 1", got)
	}
	t.Run("gather", TestCrossConcatenationEnvelopeAndValues)
	if got := connections.Load(); got != 0 {
		t.Fatalf("gather opened %d SQLite connections, want 0", got)
	}
}

func TestQueryFindsSystemPromptWithStrictInputEnabled(t *testing.T) {
	fixture := fixtureInstallation(t)
	writeConfig(t, fixture.home, "[features]\nstrict_input = true\nvector = false\n")
	runRoot(t, contractBuild(), "store", "--layer", "discovery",
		"--content", "system prompt lab marker", "--origin", "agent")
	out, err := runRootErr(t, contractBuild(), nil, "query", "system prompt", "--json")
	if err != nil {
		t.Fatalf("query: %v\n%s", err, out)
	}
	doc := mustJSON(t, out)
	if doc["row_count"] == float64(0) || !strings.Contains(out, "system prompt lab marker") {
		t.Fatalf("query missed lab memory: %s", out)
	}
}
