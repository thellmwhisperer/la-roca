package cli

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestCoreMemoriesFollowServingLayout(t *testing.T) {
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	corePath := filepath.Join(home, "roca.db")
	const content = "Synthetic orchard compass note"
	seedLayoutMemory(t, corePath, content)
	env := &cliEnv{dbPath: corePath, out: io.Discard, errOut: io.Discard,
		build: Build{Version: "v-test", Commit: "fixture"}}
	paths, err := env.resolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		name    string
		layout  string
		migrate bool
		visible bool
	}{
		{name: "before migration", layout: "legacy-serving"},
		{name: "migrated legacy", layout: "legacy-serving", migrate: true},
		{name: "cutover", layout: "cutover", visible: true},
		{name: "back to legacy", layout: "legacy-serving"},
	} {
		t.Run(state.name, func(t *testing.T) {
			if state.migrate {
				if code, err := executeWithEnv(env, []string{"--db-path", corePath, "migrate"}, nil); err != nil || code != ExitOK {
					t.Fatalf("migration: code=%d err=%v", code, err)
				}
			}
			if err := os.WriteFile(paths.Config, []byte("[features]\nroca_ops = true\n[layout]\nserving = \""+state.layout+"\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, readOnly := range []bool{false, true} {
				env.forceReadOnly = readOnly
				svc, _, err := env.openService()
				if err != nil {
					t.Fatal(err)
				}
				result, execErr := svc.Exec(t.Context(), service.ExecRequest{
					SQL: "SELECT content FROM plugin_roca_ops.core_memories",
				})
				_, ordinaryErr := svc.Exec(t.Context(), service.ExecRequest{
					SQL: "SELECT content FROM plugin_roca_ops.memories",
				})
				if err := svc.Close(); err != nil {
					t.Fatal(err)
				}
				if ordinaryErr != nil {
					t.Fatalf("ordinary ops read: %v", ordinaryErr)
				}
				if state.visible {
					if execErr != nil || result.RowCount != 1 || result.Rows[0]["content"] != content {
						t.Fatalf("cutover memory: result=%+v err=%v", result, execErr)
					}
				} else if execErr == nil || !strings.Contains(execErr.Error(), "core_memories") {
					t.Fatalf("legacy memory read: result=%+v err=%v", result, execErr)
				}
			}
			env.forceReadOnly = false
			if err := env.refreshVectorRegistry(); err != nil {
				t.Fatal(err)
			}
			registry, err := plugin.LoadVectorRegistry(plugin.VectorRegistryPath(pluginRoot(paths)))
			if err != nil {
				t.Fatal(err)
			}
			foundOps := false
			for _, database := range registry.Databases {
				if database.Plugin != "roca-ops" {
					continue
				}
				foundOps = true
				want := []string{"memories"}
				if state.visible {
					want = append(want, "core_memories")
				}
				var names []string
				for _, table := range database.Tables {
					names = append(names, table.Name)
				}
				if !slices.Equal(names, want) {
					t.Fatalf("ops vector tables = %v, want %v", names, want)
				}
			}
			if !foundOps {
				t.Fatal("vector registry has no ops sidecar")
			}
			catalog, err := env.composedCatalogSkill()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(catalog, "plugin_roca_ops.core_memories") != state.visible {
				t.Fatalf("catalog core_memories visibility should be %t", state.visible)
			}
		})
	}
}
