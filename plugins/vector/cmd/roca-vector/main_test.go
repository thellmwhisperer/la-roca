package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
	_ "modernc.org/sqlite"
)

func TestInstallLaunchesThePluginBinaryIntoManifestOwnedState(t *testing.T) {
	oldLaunch, oldExecutable := launchWorker, currentExecutable
	t.Cleanup(func() { launchWorker, currentExecutable = oldLaunch, oldExecutable })
	var request vector.LaunchRequest
	launchWorker = func(got vector.LaunchRequest) (vector.LaunchResult, error) {
		request = got
		return vector.LaunchResult{PID: 42, LogPath: filepath.Join(got.DataDir, vector.WorkerLogFilename)}, nil
	}
	currentExecutable = func() (string, error) { return "/synthetic/roca-vector", nil }
	state := filepath.Join(t.TempDir(), "state")
	env := &environment{dbPath: "/synthetic/roca.db", stateDir: state}
	root := rootCommand(env)
	root.SetArgs([]string{"install"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	want := workerArguments(env.dbPath, state, vector.DefaultModel, nil)
	if request.Executable != "/synthetic/roca-vector" || request.DataDir != state ||
		!slices.Equal(request.Arguments, want) {
		t.Fatalf("launch request = %+v, want args %q", request, want)
	}
}

func TestDeltaFlagAndReadOnlyBoundaryAreExplicit(t *testing.T) {
	env := &environment{stateDir: t.TempDir()}
	root := rootCommand(env)
	root.SetArgs([]string{"ingest"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--delta") {
		t.Fatalf("ingest without delta = %v", err)
	}
	t.Setenv("ROCA_READ_ONLY", "1")
	root = rootCommand(env)
	root.SetArgs([]string{"install"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ROCA_READ_ONLY") {
		t.Fatalf("install under read-only = %v", err)
	}
	root = rootCommand(env)
	root.SetArgs([]string{"compact"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "ROCA_READ_ONLY") {
		t.Fatalf("compact under read-only = %v", err)
	}
	if flag := ingestCommand(env).Flags().Lookup("source"); flag == nil {
		t.Fatal("targeted delta has no --source flag")
	}
	if flag := queryCommand(env).Flags().Lookup("databases"); flag == nil {
		t.Fatal("federated query has no --databases flag")
	}
}

func TestTargetedSessionDeltaIsObservableAndIdempotentThroughCLI(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	vectorPath := filepath.Join(state, vector.DatabaseFilename)
	if err := os.WriteFile(vectorPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var embedded []string
	ollama := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode embedding request: %v", err)
			response.WriteHeader(http.StatusBadRequest)
			return
		}
		embedded = append(embedded, body.Input...)
		vectors := make([][]float32, len(body.Input))
		for index := range vectors {
			vectors[index] = []float32{1, 0, 0, 0, 0, 0, 0, 0}
		}
		if err := json.NewEncoder(response).Encode(map[string]any{"embeddings": vectors}); err != nil {
			t.Errorf("encode embedding response: %v", err)
		}
	}))
	t.Cleanup(ollama.Close)
	t.Setenv("OLLAMA_HOST", ollama.URL)

	core := filepath.Join(t.TempDir(), "roca")
	coreScript := `#!/bin/sh
for argument do statement="$argument"; done
case "$statement" in
  *plugin_roca_corpus.sessions*)
    printf '%s\n' '{"rows":[{"session_id":"session-clean","title":"Public health research {\"source_exchange_fingerprints\":[\"0123456789abcdef0123456789abcdef\"],\"enabled\":true}","project_name":"health-project"}]}'
    ;;
  *)
    printf '%s\n' "unexpected non-session query: $statement" >&2
    exit 2
    ;;
esac
`
	if err := os.WriteFile(core, []byte(coreScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", core)

	env := &environment{dbPath: filepath.Join(t.TempDir(), "roca.db"), stateDir: state}
	first := executeForOutput(t, env, "ingest", "--delta", "--source", "sessions")
	if !strings.Contains(first, "1 added") {
		t.Fatalf("initial targeted delta output = %q", first)
	}

	db, err := sql.Open("sqlite", vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	clean := "Public health research\nhealth-project"
	legacyDigest := sha256.Sum256([]byte(clean))
	if _, err := db.Exec(`UPDATE chunks SET fingerprint=? WHERE source_kind='sessions'`,
		hex.EncodeToString(legacyDigest[:])); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chunks(source_kind,source_id,chunk_index,fingerprint,locator)
		VALUES ('exchanges','exchanges/session-clean/1',0,'sentinel','{}')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repaired := executeForOutput(t, env, "ingest", "--delta", "--source", "sessions")
	steady := executeForOutput(t, env, "ingest", "--delta", "--source", "sessions")
	if !strings.Contains(repaired, "1 updated") || !strings.Contains(steady, "1 unchanged") {
		t.Fatalf("repair=%q steady=%q", repaired, steady)
	}
	if len(embedded) != 2 || embedded[0] != vector.DocumentPrefix+clean || embedded[1] != vector.DocumentPrefix+clean {
		t.Fatalf("embedded session inputs = %q", embedded)
	}
	for _, contaminant := range []string{"source_exchange_fingerprints", "0123456789abcdef", "enabled", "{"} {
		if strings.Contains(strings.Join(embedded, "\n"), contaminant) {
			t.Fatalf("embedded session input contains %q: %q", contaminant, embedded)
		}
	}

	db, err = sql.Open("sqlite", vectorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var exchangeRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chunks WHERE source_kind='exchanges' AND fingerprint='sentinel'`).Scan(&exchangeRows); err != nil {
		t.Fatal(err)
	}
	if exchangeRows != 1 {
		t.Fatalf("targeted session delta changed %d non-session sentinel rows", 1-exchangeRows)
	}

	t.Logf("initial CLI: %s", strings.TrimSpace(first))
	t.Logf("repair CLI: %s", strings.TrimSpace(repaired))
	t.Logf("repeat CLI: %s", strings.TrimSpace(steady))
	t.Log(`source title: "Public health research {\"source_exchange_fingerprints\":[\"0123456789abcdef0123456789abcdef\"],\"enabled\":true}"`)
	t.Logf("embedding input: %q", embedded[1])
	t.Log("non-session sentinel: preserved")
}

func executeForOutput(t *testing.T, env *environment, args ...string) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	output := make(chan []byte, 1)
	go func() {
		raw, _ := io.ReadAll(read)
		output <- raw
	}()

	command := rootCommand(env)
	command.SetArgs(args)
	executeErr := command.ExecuteContext(context.Background())
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = original
	raw := <-output
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	if executeErr != nil {
		t.Fatalf("execute %q: %v", args, executeErr)
	}
	return string(raw)
}

func TestDefaultStateDirUsesThePluginIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	got, err := (&environment{}).resolveStateDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".roca", "plugins", "roca-vector", "state")
	if got != want {
		t.Fatalf("default state dir = %q, want %q", got, want)
	}
}

func TestVectorRegistryRemainsHomeScopedForACustomCoreDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", "")
	env := &environment{dbPath: filepath.Join(t.TempDir(), "custom.db")}
	root, err := env.resolvePluginRoot()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".roca", "plugins"); root != want {
		t.Fatalf("vector registry root = %q, want %q", root, want)
	}
}

func TestWorkerCarriesExplicitCoreAndStatePaths(t *testing.T) {
	got := workerArguments("/synthetic/roca.db", "/synthetic/state", "synthetic-model", nil)
	want := []string{"--state-dir", "/synthetic/state", "--db-path", "/synthetic/roca.db",
		"_worker", "--model", "synthetic-model"}
	if !slices.Equal(got, want) {
		t.Fatalf("worker arguments = %q, want %q", got, want)
	}
}
