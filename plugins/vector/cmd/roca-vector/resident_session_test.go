package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

type residentTestEmbedder struct {
	stubEmbedder
	warms       int
	prewarmErr  error
	terminalErr error
}

func (e *residentTestEmbedder) Prewarm(context.Context) error {
	e.warms++
	return e.prewarmErr
}

func (e *residentTestEmbedder) TerminalError() error { return e.terminalErr }

func TestResidentSessionRejectsFailedPrewarmAndAllowsFreshStart(t *testing.T) {
	missing := errors.New("model absent")
	if _, err := residentSessionWithEmbedder(context.Background(), &environment{}, &residentTestEmbedder{prewarmErr: missing}, nil); !errors.Is(err, missing) {
		t.Fatalf("failed prewarm retained a session: %v", err)
	}
	embedder := &residentTestEmbedder{}
	session, err := residentSessionWithEmbedder(context.Background(), &environment{}, embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.waitReady(context.Background()); err != nil || embedder.warms != 1 {
		t.Fatalf("fresh resident was not ready: %v, warms=%d", err, embedder.warms)
	}
}

func TestResidentSessionRefreshesRegistryWithoutRewarming(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell subprocess fixture")
	}
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(plugins, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", plugins)
	writeRegistry := func(names ...string) {
		t.Helper()
		databases := []map[string]any{}
		for _, name := range names {
			databases = append(databases, map[string]any{
				"plugin": "fixture", "database": name, "path": name + ".db", "alias": "fixture_" + name,
				"tables": []map[string]any{{"name": "records", "id_column": "id", "text_columns": []string{"body"}}},
			})
		}
		body, err := json.Marshal(map[string]any{"schema": 2, "databases": databases})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plugins, "vector-registry.json"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry("old")
	script := filepath.Join(root, "roca")
	body := `#!/bin/sh
printf '%s' '{"databases":["newdb"],"selected":[{"source":"plugin:fixture/newdb","database":"newdb"}]}'
`
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", script)
	env := &environment{dbPath: filepath.Join(root, "roca.db"), stateDir: filepath.Join(root, "state")}
	embedder := &residentTestEmbedder{}
	session, err := residentSessionWithEmbedder(context.Background(), env, embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	query := func() vector.FederatedQuery {
		t.Helper()
		result, err := session.query(context.Background(), residentRequest{Query: "harbor", K: 3})
		if err != nil {
			t.Fatal(err)
		}
		return result.(vector.FederatedQuery)
	}
	before := query()
	if !strings.Contains(strings.Join(before.Notices, " "), "no vector declaration") {
		t.Fatalf("undeclared database result = %+v", before)
	}
	writeRegistry("old", "newdb")
	after := query()
	if notices := strings.Join(after.Notices, " "); strings.Contains(notices, "no vector declaration") || !strings.Contains(notices, "no ready vector sidecar") {
		t.Fatalf("new database was not discovered: %+v", after)
	}
	if embedder.warms != 1 {
		t.Fatalf("registry refresh reloaded the model %d times", embedder.warms)
	}
	embedder.terminalErr = errors.New("embedding runtime failed")
	if _, err := session.query(context.Background(), residentRequest{Query: "harbor", K: 3}); !errors.Is(err, errResidentUnusable) {
		t.Fatalf("terminal embedding failure did not retire session: %v", err)
	}
}
