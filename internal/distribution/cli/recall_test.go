package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestPersistsRecallProvenanceForQualifiedExec(t *testing.T) {
	home, _, _, dbPath := initializedIngestCLI(t)
	writeConfig(t, home, "[features]\nroca_ops = true\n")
	writeFile(t, filepath.Join(home, ".roca", "logs", "recall.jsonl"), `{"ts":"2026-09-22T10:00:00Z","action":"agent spawn","tool":"Agent","query_sha":"abc123","query":"preserve the decision","session_id":"session-1","exchange_id":42,"raw":4,"hits":1,"top_score":0.812,"ids":["7"],"scores":[0.812],"dates":["2026-09-22"],"cands":1,"elapsed_ms":12}`+"\n")

	run := executeHermeticCLI([]string{"ingest", "--db-path", dbPath})
	if run.err != nil || run.code != ExitOK {
		t.Fatalf("ingest = code %d err %v:\n%s%s", run.code, run.err, run.output, run.warnings)
	}

	query := executeHermeticCLI([]string{"--db-path", dbPath, "--json", "exec", "SELECT query, session_id, exchange_id FROM plugin_roca_ops.recall_events WHERE query_sha = 'abc123'"})
	if query.err != nil || query.code != ExitOK {
		t.Fatalf("qualified recall exec = code %d err %v:\n%s%s", query.code, query.err, query.output, query.warnings)
	}
	for _, want := range []string{"preserve the decision", "session-1", "42", "plugin_roca_ops"} {
		if !strings.Contains(query.output, want) {
			t.Fatalf("qualified recall exec lacks %q:\n%s%s", want, query.output, query.warnings)
		}
	}
	t.Logf("qualified recall_events row persisted and readable through roca exec:\n%s", query.output)
}
