package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
	"github.com/thellmwhisperer/la-roca/pkg/vectorhelp"
	_ "modernc.org/sqlite"
)

func TestStatusCommandReportsAXIRowsWithoutWaitingForTheModel(t *testing.T) {
	root := t.TempDir()
	pluginRoot := filepath.Join(root, "plugins")
	state := filepath.Join(pluginRoot, "roca-vector", "state")
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", pluginRoot)
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", "/synthetic/roca")
	claim, err := vector.EncodeWorkerClaim(os.Getpid(), "status-cli")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, vector.WorkerClaimFilename), claim, 0o600); err != nil {
		t.Fatal(err)
	}
	releaseClaim, err := vector.LockWorkerClaim(state)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseClaim()

	registry := `{
		"schema": 2,
		"databases": [
			{"plugin":"claude-code-parser","database":"corpus","path":"claude-code-corpus.db","alias":"parser","tables":[{"name":"sessions","id_column":"session_id","text_columns":["title"]}]},
			{"plugin":"roca-corpus","database":"corpus","path":"roca-corpus.db","alias":"corpus","tables":[{"name":"notes","id_column":"id","text_columns":["body"]}]},
			{"plugin":"roca-notes","database":"notes","path":"notes.db","alias":"notes","tables":[{"name":"task_state_versions","id_column":"id","text_columns":["body"]}]},
			{"plugin":"roca-galactic","database":"galactic","path":"roca-galactic.db","alias":"galactic","tables":[{"name":"messages","id_column":"id","text_columns":["body"]}]},
			{"plugin":"roca-ops","database":"ops","path":"roca-ops.db","alias":"ops","tables":[{"name":"memories","id_column":"id","text_columns":["content"]}]}
		]
	}`
	if err := os.WriteFile(filepath.Join(pluginRoot, "vector-registry.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(pluginRoot, "roca-corpus", "roca-corpus.vector.db")
	if err := os.MkdirAll(filepath.Dir(sidecar), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := vector.InitOwnedSidecar(sidecar, "roca-corpus/corpus", vector.DefaultModel); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chunks(source_kind,source_id,text_column,chunk_index,fingerprint,locator)
		VALUES('notes','a','body',0,'fp','loc')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	oldEmbedder := newEmbedder
	t.Cleanup(func() { newEmbedder = oldEmbedder })
	newEmbedder = func(*environment) vector.Embedder {
		t.Fatal("status waited for the embedding model")
		return stubEmbedder{}
	}

	env := &environment{dbPath: filepath.Join(dataDir, "roca.db"), stateDir: state, json: true}
	command := rootCommand(env)
	stdout := &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(stdout)
	command.SetArgs([]string{"--json", "status"})
	started := time.Now()
	if err := command.Execute(); err != nil {
		t.Fatalf("status --json: %v\n%s", err, stdout.String())
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("status --json blocked for %s", time.Since(started))
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("status --json is not an envelope: %v\n%s", err, stdout.String())
	}
	if envelope["help"] == nil {
		t.Fatalf("JSON envelope missing help[]: %s", stdout.String())
	}
	databases, _ := envelope["databases"].([]any)
	if len(databases) != 5 {
		t.Fatalf("JSON databases = %d, want 5: %s", len(databases), stdout.String())
	}

	env = &environment{dbPath: filepath.Join(dataDir, "roca.db"), stateDir: state}
	command = rootCommand(env)
	stdout = &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(stdout)
	command.SetArgs([]string{"status"})
	if err := command.Execute(); err != nil {
		t.Fatalf("status: %v\n%s", err, stdout.String())
	}
	text := stdout.String()
	for _, needle := range []string{
		"worker:", "databases[5]{", "plugin", "embedded_chunks", "candidate_chunks",
		"sidecar_bytes", "last_write", "state", "index_lock", "compact_recommended",
		"help[", "roca vector status --json",
		"claude-code-parser", "roca-corpus", "roca-notes", "roca-galactic", "roca-ops",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("AXI status missing %q:\n%s", needle, text)
		}
	}
	if strings.Contains(text, "progress unavailable") || strings.Contains(text, "read 0 total 0") {
		t.Fatalf("status still lies:\n%s", text)
	}
}

func TestStatusHelpSuggestsInstallOnlyWhenChunksAreMissing(t *testing.T) {
	chunks := int64(12)
	zero := int64(0)
	report := vector.Vectorization{
		Databases: []vector.DatabaseVectorization{
			{Plugin: "roca-ops", Database: "ops", State: vector.StateComplete, EmbeddedChunks: &chunks, IndexLock: vector.IndexLockUnheld},
			{Plugin: "roca-corpus", Database: "corpus", State: vector.StateEmpty, EmbeddedChunks: &zero},
		},
	}
	help := statusHelp(report)
	joined := strings.Join(help, "\n")
	if !strings.Contains(joined, "roca vector install") {
		t.Fatalf("zero chunks did not suggest install: %v", help)
	}
	if strings.Contains(joined, "stale") {
		t.Fatalf("unheld lock still narrated as stale: %v", help)
	}

	completeOnly := vector.Vectorization{
		Databases: []vector.DatabaseVectorization{
			{Plugin: "roca-ops", Database: "ops", State: vector.StateComplete, EmbeddedChunks: &chunks, IndexLock: vector.IndexLockUnheld},
		},
	}
	help = statusHelp(completeOnly)
	joined = strings.Join(help, "\n")
	if strings.Contains(joined, "roca vector install") {
		t.Fatalf("embedded sidecar still suggested install: %v", help)
	}

	locked := vector.Vectorization{
		Databases: []vector.DatabaseVectorization{
			{Plugin: "roca-ops", Database: "ops", State: vector.StateComplete, EmbeddedChunks: &chunks, IndexLock: vector.IndexLockHeld},
			{Plugin: "roca-notes", Database: "notes", State: vector.StateUnknown, IndexLock: vector.IndexLockError},
		},
	}
	help = statusHelp(locked)
	joined = strings.Join(help, "\n")
	if !strings.Contains(joined, "index.lock is held on roca-ops/ops") {
		t.Fatalf("held lock hint missing: %v", help)
	}
	if !strings.Contains(joined, "could not inspect index.lock on roca-notes/notes") {
		t.Fatalf("inspection error hint missing: %v", help)
	}
}

func TestQueryHelpUsesSharedHints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result vector.FederatedQuery
		shared []vectorhelp.Hit
		want   string
	}{
		{
			name: "declared read shape",
			result: vector.FederatedQuery{Results: []vector.Result{{
				Database: "corpus", Table: "exchanges", ID: "42", Alias: "plugin_roca_corpus",
				IDColumn: "id", TextColumns: []string{"human_text", "agent_text"},
			}}},
			shared: []vectorhelp.Hit{{
				Alias: "plugin_roca_corpus", Table: "exchanges", ID: "42",
				IDColumn: "id", TextColumns: []string{"human_text", "agent_text"},
			}},
			want: `SELECT human_text, agent_text FROM plugin_roca_corpus.exchanges WHERE id = '42'`,
		},
		{
			name: "escaped id",
			result: vector.FederatedQuery{Results: []vector.Result{{
				Table: "records", ID: "a'b", Alias: "plugin_fixture_records",
				IDColumn: "record_key", TextColumns: []string{"body", "title"},
			}}},
			shared: []vectorhelp.Hit{{
				Alias: "plugin_fixture_records", Table: "records", ID: "a'b",
				IDColumn: "record_key", TextColumns: []string{"body", "title"},
			}},
			want: `SELECT body, title FROM plugin_fixture_records.records WHERE record_key = 'a''b'`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			help := queryHelp(tc.result)
			shared := vectorhelp.Query(tc.shared)
			if !slices.Equal(help, shared) {
				t.Fatalf("plugin help = %v, shared = %v", help, shared)
			}
			if !strings.Contains(strings.Join(help, "\n"), tc.want) {
				t.Fatalf("vector query help = %v", help)
			}
		})
	}
}

func TestStatusCommandReturnsInspectErrorWithoutPrintingRows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", filepath.Join(root, "missing-plugins"))
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", "/synthetic/roca")
	command := statusCommand(&environment{
		dbPath:   filepath.Join(root, "roca.db"),
		stateDir: filepath.Join(root, "state"),
	})
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	if err := command.Execute(); err == nil {
		t.Fatal("status succeeded without a registry")
	}
	if strings.Contains(out.String(), "databases[") {
		t.Fatalf("failed status printed rows: %q", out.String())
	}
}
