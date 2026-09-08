package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/model"
	"github.com/thellmwhisperer/la-roca-vector/internal/vector"
)

type residentTestEmbedder struct {
	stubEmbedder
	warms       int
	prewarmErr  error
	terminalErr error
}

type downloadingResidentEmbedder struct {
	residentTestEmbedder
}

func (e *downloadingResidentEmbedder) Embed(context.Context, string, []string) ([][]float32, error) {
	return nil, e.prewarmErr
}

func TestResidentQueryDuringModelDownloadReturnsNotices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix resident and shell fixture")
	}
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(plugins, "fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema":2,"databases":[{"plugin":"fixture","database":"records","path":"records.db","alias":"fixture_records","tables":[{"name":"records","id_column":"id","text_columns":["body"]}]}]}`
	if err := os.WriteFile(filepath.Join(plugins, "vector-registry.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_PLUGIN_ROOT", plugins)
	script := filepath.Join(root, "roca")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' '{\"databases\":[\"records\"],\"selected\":[{\"source\":\"plugin:fixture/records\",\"database\":\"records\"}]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_VECTOR_ROCA_BINARY", script)
	sidecar := vector.SidecarPath(filepath.Join(plugins, "fixture", "records.db"))
	if err := vector.InitOwnedSidecar(sidecar, "fixture/records", vector.DefaultModel); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sidecar)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('dimensions','8')`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, missing := model.Existing(root, model.DefaultManifest())
	if !errors.Is(missing, model.ErrNotDownloaded) {
		t.Fatalf("missing model = %v", missing)
	}
	embedder := &downloadingResidentEmbedder{residentTestEmbedder: residentTestEmbedder{prewarmErr: missing}}
	env := &environment{dbPath: filepath.Join(root, "roca.db"), stateDir: filepath.Join(root, "state")}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := residentSessionWithEmbedder(ctx, env, embedder, nil)
	if err != nil {
		t.Fatal(err)
	}
	listener := boundResident(t)
	t.Setenv("ROCA_VECTOR_RESIDENT_SOCKET", listener.Addr().String())
	go func() { _ = serveListeningResident(ctx, listener, time.Minute, session) }()
	result, used, err := env.queryThroughResident(ctx, "harbor", 3, "records", true, 0.35)
	if err != nil {
		t.Fatal(err)
	}
	if !used || result.VectorExecuted || len(result.Results) != 0 || !strings.Contains(strings.Join(result.Notices, " "), "embedding model is not downloaded") {
		t.Fatalf("query while downloading = %+v, resident=%v", result, used)
	}
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
		selected := []vector.DatabaseSelection{}
		for _, name := range names {
			selected = append(selected, vector.DatabaseSelection{Source: "plugin:fixture/" + name, Database: name})
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
		scope, err := json.Marshal(vector.DatabaseScope{Databases: names, Selected: selected})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(plugins, "scope.json"), scope, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry("old")
	script := filepath.Join(root, "roca")
	body := `#!/bin/sh
cat "$ROCA_VECTOR_PLUGIN_ROOT/scope.json"
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
		result, err := session.query(context.Background(), residentRequest{Query: "harbor", K: 3, Databases: "all"})
		if err != nil {
			t.Fatal(err)
		}
		return result.(vector.FederatedQuery)
	}
	before := query()
	if !slices.Equal(before.Databases, []string{"old"}) || !strings.Contains(strings.Join(before.Notices, " "), "database old has no ready vector sidecar") {
		t.Fatalf("initial database result = %+v", before)
	}
	writeRegistry("old", "newdb")
	after := query()
	if notices := strings.Join(after.Notices, " "); !slices.Equal(after.Databases, []string{"old", "newdb"}) || strings.Contains(notices, "no vector declaration") || !strings.Contains(notices, "database newdb has no ready vector sidecar") {
		t.Fatalf("new database was not discovered: %+v", after)
	}
	if embedder.warms != 1 {
		t.Fatalf("registry refresh reloaded the model %d times", embedder.warms)
	}
	embedder.terminalErr = errors.New("embedding runtime failed")
	if _, err := session.query(context.Background(), residentRequest{Query: "harbor", K: 3, Databases: "all"}); !errors.Is(err, errResidentUnusable) {
		t.Fatalf("terminal embedding failure did not retire session: %v", err)
	}
}
