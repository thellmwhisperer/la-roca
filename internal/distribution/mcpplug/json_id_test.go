package mcpplug_test

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestStoreMetadataAndSupersedesKeepOpsIdsAsStrings(t *testing.T) {
	svc := seededOpsService(t)
	session := connectAs(t, svc, "claude-code", "2.1.0")

	created := callTool(t, session, "roca_store", map[string]any{
		"layer": "discovery", "content": "ops identifier through the plug",
	})
	id, ok := created.Meta["id"].(string)
	if !ok || id == "" {
		t.Fatalf("MCP store metadata id = %#v, want a decimal string", created.Meta["id"])
	}
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		t.Fatalf("MCP store id %q is not an integer: %v", id, err)
	}

	execed := callTool(t, session, "roca_exec", map[string]any{
		"sql": "SELECT id FROM plugin_roca_ops.memories LIMIT 1",
	})
	text := renderedText(execed)
	if !strings.Contains(text, `"`+id+`"`) {
		t.Fatalf("MCP exec TOON did not quote the ops id:\n%s", text)
	}

	replaced := callTool(t, session, "roca_store", map[string]any{
		"layer": "discovery", "content": "ops identifier replacement through the plug",
		"supersedes": id,
	})
	if replaced.Meta["id"] == id {
		t.Fatal("string supersedes rewrote the original id")
	}
	counted, err := svc.Exec(t.Context(), service.ExecRequest{
		SQL: "SELECT COUNT(*) AS n FROM plugin_roca_ops.memories WHERE supersedes = " + id,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counted.RowCount != 1 || fmt.Sprint(counted.Rows[0]["n"]) != "1" {
		t.Fatalf("string supersedes updated %#v, want 1 row", counted.Rows)
	}

	refused := callToolExpectingError(t, session, "roca_store", map[string]any{
		"layer": "discovery", "content": "numeric supersedes still works",
		"supersedes": 42,
	})
	if !strings.Contains(refused, "42") {
		t.Fatalf("numeric supersedes was not accepted as an integer: %s", refused)
	}
}

func TestStoreSupersedesSchemaAdvertisesAString(t *testing.T) {
	session := connect(t, seededService(t))
	for _, tool := range listTools(t, session).Tools {
		if tool.Name != "roca_store" {
			continue
		}
		schema, _ := tool.InputSchema.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		supersedes, _ := properties["supersedes"].(map[string]any)
		if !schemaAllowsString(supersedes["type"]) {
			t.Fatalf("supersedes schema = %#v, want string or string|integer", supersedes)
		}
		return
	}
	t.Fatal("roca_store is not listed")
}

func seededOpsService(t *testing.T) *service.Service {
	t.Helper()
	dir := theDirectoryOf(t)
	plugins := filepath.Join(dir, "plugins")
	if _, err := rocaops.Ensure(plugins, filepath.Join(dir, "bin"), "0.0.0-test"); err != nil {
		t.Fatal(err)
	}
	svc, err := service.Open(service.Options{
		DBPath:         filepath.Join(dir, "roca.db"),
		BackupDir:      filepath.Join(dir, "backups"),
		DataDir:        dir,
		PluginDir:      plugins,
		RocaOpsEnabled: true,
		Version:        "0.0.0-test",
		Commit:         "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Init(t.Context()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return svc
}

func schemaAllowsString(typ any) bool {
	switch typed := typ.(type) {
	case string:
		return typed == "string"
	case []any:
		for _, item := range typed {
			if item == "string" {
				return true
			}
		}
	}
	return false
}
