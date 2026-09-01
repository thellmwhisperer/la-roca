package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
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
	if err := os.WriteFile(filepath.Join(state, vector.WorkerClaimFilename), []byte(fmt.Sprintf("%d status-cli\n", os.Getpid())), 0o600); err != nil {
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
		"sidecar_bytes", "last_write", "state", "help[", "roca vector status --json",
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
