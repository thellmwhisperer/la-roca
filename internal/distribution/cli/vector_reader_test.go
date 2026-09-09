package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"strings"
	"testing"
)

func TestVectorReaderKeepsGateScopeAndRecoversAfterRejectedRequests(t *testing.T) {
	fixtureInstallation(t)
	runRoot(t, contractBuild(), "store", "--layer", "discovery", "--content", "synthetic reader marker", "--origin", "agent")
	requests := []struct {
		request   map[string]any
		wantError bool
	}{
		{map[string]any{"scope": true, "databases": "ops"}, false},
		{map[string]any{"scope": true, "databases": "not-installed"}, true},
		{map[string]any{"sql": "DELETE FROM plugin_roca_ops.memories"}, true},
		{map[string]any{"sql": "SELECT content FROM memories"}, true},
		{map[string]any{"sql": "SELECT content FROM plugin_roca_ops.memories WHERE content='synthetic reader marker'"}, false},
		{map[string]any{"sql": "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n", "timeout_ms": 1}, true},
		{map[string]any{"sql": "SELECT 42 AS answer"}, false},
		{map[string]any{"cursor": "bad", "sql": "DELETE FROM plugin_roca_ops.memories"}, true},
		{map[string]any{"cursor": "hidden", "sql": "SELECT name FROM sqlite_master"}, true},
		{map[string]any{"cursor": "sweep", "sql": "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1501) SELECT x FROM n"}, false},
		{map[string]any{"cursor": "sweep"}, false},
		{map[string]any{"cursor": "sweep"}, false},
		{map[string]any{"cursor": "sweep"}, false},
		{map[string]any{"sql": "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1501) SELECT x FROM n"}, false},
	}
	var input strings.Builder
	encoder := json.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request.request); err != nil {
			t.Fatal(err)
		}
	}
	output, err := runRootErr(t, contractBuild(), strings.NewReader(input.String()), "_vector-reader")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	for index, request := range requests {
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("response %d: %v: %s", index, err, output)
		}
		if (response.Error != "") != request.wantError {
			t.Fatalf("response %d: %s: %s", index, response.Result, response.Error)
		}
		if index >= 9 {
			var result struct {
				Rows []struct {
					X int `json:"x"`
				} `json:"rows"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatal(err)
			}
			want := 500
			if index == 12 {
				want = 1
			}
			if index == 13 {
				want = 1000
			}
			if len(result.Rows) != want {
				t.Fatalf("page %d: %d rows, want %d", index, len(result.Rows), want)
			}
			first := 1
			if index < 13 {
				first += (index - 9) * 500
			}
			for offset, row := range result.Rows {
				if row.X != first+offset {
					t.Fatalf("cursor lost position: %+v", row)
				}
			}
		}
		switch index {
		case 0:
			var scope struct {
				Databases []string `json:"databases"`
			}
			if err := json.Unmarshal(response.Result, &scope); err != nil || len(scope.Databases) != 1 || scope.Databases[0] != "ops" {
				t.Fatalf("scope: %s: %v", response.Result, err)
			}
		case 4, 6:
			var result struct {
				Rows []map[string]any `json:"rows"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil || len(result.Rows) != 1 {
				t.Fatalf("rows: %s: %v", response.Result, err)
			}
			if index == 4 && result.Rows[0]["content"] != "synthetic reader marker" {
				t.Fatalf("visible row lost: %+v", result.Rows)
			}
			if index == 6 && result.Rows[0]["answer"] != float64(42) {
				t.Fatalf("reader did not recover: %+v", result.Rows)
			}
		}
	}
}

func TestVectorReaderInterleavesCutoverCursorsAndRequests(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	writeConfig(t, home, "[layout]\nserving = \"cutover\"\n")
	setup := &cliEnv{dbPath: filepath.Join(home, ".roca", "roca.db"), out: io.Discard, errOut: io.Discard, build: contractBuild()}
	svc, _, err := setup.openService()
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	if _, err := os.Stat(setup.dbPath); !os.IsNotExist(err) {
		t.Fatalf("cutover fixture touched the legacy database: %v", err)
	}
	ops := openLayoutDatabase(t, filepath.Join(home, ".roca", "plugins", rocaops.Name, rocaops.DatabaseFilename))
	if _, err := ops.Exec("INSERT INTO memories(layer, content, origin) VALUES('discovery', 'synthetic neighbour', 'agent')"); err != nil {
		ops.Close()
		t.Fatal(err)
	}
	ops.Close()
	db := openLayoutDatabase(t, filepath.Join(home, ".roca", "plugins", rocacorpus.Name, rocacorpus.DatabaseFilename))
	for _, statement := range []string{
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1001)
   INSERT INTO sessions(session_id, title) SELECT CAST(x AS TEXT), 'synthetic session ' || x FROM n`,
		`INSERT INTO exchanges(session_id, exchange_number, human_text)
   SELECT session_id, CAST(session_id AS INTEGER), 'synthetic exchange ' || session_id FROM sessions`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	requests := []struct {
		request   map[string]any
		count     int
		first     int
		wantError bool
	}{
		{map[string]any{"cursor": "sessions", "sql": "SELECT CAST(session_id AS INTEGER) AS position FROM plugin_roca_corpus.sessions ORDER BY position"}, 500, 1, false},
		{map[string]any{"cursor": "exchanges", "sql": "SELECT exchange_number AS position FROM plugin_roca_corpus.exchanges ORDER BY position"}, 500, 1, false},
		{map[string]any{"sql": "SELECT content FROM plugin_roca_ops.memories WHERE content='synthetic neighbour'"}, 1, 0, false},
		{map[string]any{"sql": "DELETE FROM plugin_roca_corpus.sessions"}, 0, 0, true},
		{map[string]any{"cursor": "sessions"}, 500, 501, false},
		{map[string]any{"cursor": "exchanges"}, 500, 501, false},
		{map[string]any{"cursor": "sessions"}, 1, 1001, false},
		{map[string]any{"cursor": "exchanges"}, 1, 1001, false},
		{map[string]any{"sql": "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n", "timeout_ms": 1}, 0, 0, true},
		{map[string]any{"sql": "SELECT COUNT(*) AS position FROM plugin_roca_corpus.sessions"}, 1, 1001, false},
	}
	var input, output strings.Builder
	encoder := json.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request.request); err != nil {
			t.Fatal(err)
		}
	}
	env := hermeticCLIEnv(&cliEnv{build: contractBuild(), out: &output, errOut: &output})
	root := rootCommand(env)
	root.SetArgs([]string{"_vector-reader"})
	root.SetIn(strings.NewReader(input.String()))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	for index, request := range requests {
		var response struct {
			Result struct {
				Rows []map[string]any `json:"rows"`
			} `json:"result"`
			Error string `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("response %d: %v", index, err)
		}
		if (response.Error != "") != request.wantError || len(response.Result.Rows) != request.count {
			t.Fatalf("response %d: rows=%d error=%q", index, len(response.Result.Rows), response.Error)
		}
		for offset, row := range response.Result.Rows {
			if request.first != 0 && fmt.Sprint(row["position"]) != fmt.Sprint(request.first+offset) {
				t.Fatalf("response %d row %d: %v", index, offset, row)
			}
			if index == 2 && row["content"] != "synthetic neighbour" {
				t.Fatalf("neighbour text: %v", row)
			}
		}
	}
}
