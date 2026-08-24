//go:build cgo && darwin

package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
	_ "modernc.org/sqlite"
)

func TestMain(m *testing.M) {
	if os.Getenv("ROCA_VECTOR_E2E_HELPER") == "1" {
		if runE2ECore(os.Args[1:]) {
			return
		}
		main()
		return
	}
	os.Exit(m.Run())
}

func runE2ECore(args []string) bool {
	for _, argument := range args {
		if argument == "_database-scope" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
				"databases": []string{"records"},
				"selected":  []map[string]string{{"source": "plugin:fixture/records", "database": "records"}},
			})
			return true
		}
	}
	if !containsArgument(args, "exec") {
		return false
	}
	statement := args[len(args)-1]
	rows := []map[string]any{}
	switch {
	case strings.Contains(statement, "COUNT(*) AS n"):
		rows = append(rows, map[string]any{"n": 1})
	case strings.Contains(statement, "SUM("):
		rows = append(rows, map[string]any{"total": 1})
	case strings.Contains(statement, `FROM "plugin_fixture"."records"`):
		rows = append(rows, map[string]any{
			"source_id": "record-1", "body": "accelerator concurrency regression",
			"context_title": "", "context_project": "", "context_time": "record-1",
		})
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"rows": rows})
	return true
}

func containsArgument(args []string, target string) bool {
	for _, argument := range args {
		if argument == target {
			return true
		}
	}
	return false
}

func TestDeltaIngestTerminatesWhileResidentHoldsAccelerator(t *testing.T) {
	labData := strings.TrimSpace(os.Getenv("ROCA_VECTOR_LAB_DATA_DIR"))
	if labData == "" {
		t.Skip("set ROCA_VECTOR_LAB_DATA_DIR to run the native concurrency regression")
	}
	sourceModel, err := model.Existing(labData, model.DefaultManifest())
	if err != nil {
		t.Skip(err.Error())
	}
	sourceModel, err = filepath.Abs(sourceModel)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	destinationModel := model.FilePath(root, model.DefaultManifest())
	if err := os.MkdirAll(filepath.Dir(destinationModel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sourceModel, destinationModel); err != nil {
		t.Fatal(err)
	}
	pluginRoot := filepath.Join(root, "plugins")
	fixtureRoot := filepath.Join(pluginRoot, "fixture")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(fixtureRoot, "records.db")
	if err := os.WriteFile(sourcePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	sidecarPath := vector.SidecarPath(sourcePath)
	if err := os.WriteFile(sidecarPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{
		"schema": 2,
		"databases": []map[string]any{{
			"plugin": "fixture", "database": "records", "path": "records.db", "alias": "plugin_fixture",
			"tables": []map[string]any{{"name": "records", "id_column": "id", "text_columns": []string{"body"}}},
		}},
		"routes": []map[string]any{{
			"plugin": "fixture", "database": "records", "alias": "plugin_fixture", "source": "plugin:fixture/records",
		}},
	}
	registryFile, err := os.Create(filepath.Join(pluginRoot, "vector-registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(registryFile).Encode(registry); err != nil {
		registryFile.Close()
		t.Fatal(err)
	}
	if err := registryFile.Close(); err != nil {
		t.Fatal(err)
	}

	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_E2E_HELPER", "1")
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", binary)
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", pluginRoot)
	t.Setenv("ROCA_READ_ONLY", "")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stateDir := filepath.Join(root, "state")
	dbPath := filepath.Join(root, "roca.db")
	resident := exec.CommandContext(ctx, binary, "--json", "--db-path", dbPath,
		"--state-dir", stateDir, "_resident")
	residentInput, err := resident.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	residentOutput, err := resident.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	residentLog, err := os.Create(filepath.Join(root, "resident.stderr"))
	if err != nil {
		t.Fatal(err)
	}
	resident.Stderr = residentLog
	if err := resident.Start(); err != nil {
		t.Fatal(err)
	}
	residentDone := false
	t.Cleanup(func() {
		_ = residentInput.Close()
		if !residentDone && resident.Process != nil {
			_ = resident.Process.Kill()
			_ = resident.Wait()
		}
		_ = residentLog.Close()
	})

	accelerated, err := awaitResidentPrewarm(ctx, residentOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !accelerated {
		t.Skip("native resident did not acquire the accelerator")
	}

	ingest := exec.CommandContext(ctx, binary, "--json", "--db-path", dbPath,
		"--state-dir", stateDir, "ingest", "--delta")
	ingestOutput, err := ingest.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("vector ingest --delta did not terminate while the accelerated resident was alive")
		}
		t.Fatalf("vector ingest --delta: %v: %s", err, strings.TrimSpace(string(ingestOutput)))
	}
	var report struct {
		Counts struct {
			Added int `json:"added"`
		} `json:"counts"`
	}
	if err := json.Unmarshal(ingestOutput, &report); err != nil {
		t.Fatalf("decode ingest output: %v: %s", err, ingestOutput)
	}
	if report.Counts.Added == 0 {
		t.Fatalf("vector ingest --delta added no embeddings: %s", ingestOutput)
	}

	store, err := sql.Open("sqlite", sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var chunks int
	if err := store.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&chunks); err != nil {
		t.Fatal(err)
	}
	if chunks == 0 {
		t.Fatal("vector ingest --delta terminated without growing the index")
	}

	if err := residentInput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := resident.Wait(); err != nil {
		t.Fatal(err)
	}
	residentDone = true
}

func awaitResidentPrewarm(ctx context.Context, output io.Reader) (bool, error) {
	type result struct {
		accelerated bool
		err         error
	}
	ready := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			var event struct {
				Kind  string         `json:"kind"`
				Stage string         `json:"stage"`
				Error string         `json:"error"`
				Extra map[string]any `json:"extra"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				ready <- result{err: fmt.Errorf("decode resident event: %w", err)}
				return
			}
			if event.Stage != "prewarm" || event.Kind == "progress" {
				continue
			}
			if event.Kind == "error" {
				ready <- result{err: fmt.Errorf("resident prewarm: %s", event.Error)}
				return
			}
			accelerated, _ := event.Extra["accelerated"].(bool)
			ready <- result{accelerated: accelerated}
			return
		}
		if err := scanner.Err(); err != nil {
			ready <- result{err: err}
			return
		}
		ready <- result{err: io.ErrUnexpectedEOF}
	}()
	select {
	case result := <-ready:
		return result.accelerated, result.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
