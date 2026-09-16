//go:build acceptance

package acceptance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealBinaryVectorReaderRecoversAfterRejectedRequests(t *testing.T) {
	binary, err := rocaBinary()
	if err != nil {
		t.Fatalf("I cannot find the binary: %v", err)
	}
	home, err := acceptanceTempDir("roca-vector-reader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, ".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}

	world := &distributionWorld{}
	init := world.runAt(home, binary, "init", "--db-path", filepath.Join(home, ".roca", "roca.db"), "--json")
	if init.code != 0 {
		t.Fatalf("initialize disposable home: code %d\n%s%s", init.code, init.stdout, init.stderr)
	}

	requests := []struct {
		body      map[string]any
		wantError bool
	}{
		{body: map[string]any{"scope": true, "databases": "ops"}},
		{body: map[string]any{"scope": true, "databases": "not-installed"}, wantError: true},
		{body: map[string]any{"sql": "DELETE FROM plugin_roca_ops.memories"}, wantError: true},
		{body: map[string]any{"sql": "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n", "timeout_ms": 1}, wantError: true},
		{body: map[string]any{"sql": "SELECT 42 AS answer"}},
	}
	var input strings.Builder
	encoder := json.NewEncoder(&input)
	for _, request := range requests {
		if err := encoder.Encode(request.body); err != nil {
			t.Fatal(err)
		}
	}
	run := world.runAtInput(home, binary, input.String(), nil, "_vector-reader")
	if run.code != 0 {
		t.Fatalf("vector reader: code %d\n%s%s", run.code, run.stdout, run.stderr)
	}

	decoder := json.NewDecoder(strings.NewReader(run.stdout))
	for index, request := range requests {
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatalf("response %d: %v\n%s", index, err, run.stdout)
		}
		if (response.Error != "") != request.wantError {
			t.Fatalf("response %d: result=%s error=%q", index, response.Result, response.Error)
		}
		if index == len(requests)-1 {
			var result struct {
				Rows []map[string]any `json:"rows"`
			}
			if err := json.Unmarshal(response.Result, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Rows) != 1 || result.Rows[0]["answer"] != float64(42) {
				t.Fatalf("reader did not recover after rejected and timed-out requests: %+v", result.Rows)
			}
		}
	}
}
