package supportreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

func TestPluginOriginRedactsFilesystemSources(t *testing.T) {
	cases := []struct {
		source, origin, reported string
	}{
		{"bundled:roca", OriginBundled, "bundled:roca"},
		{"owner/repo", OriginExternal, OriginRemote},
		{"https://example.com/plugin.git", OriginExternal, "example.com"},
		{"https://private-user:secret@example.com/plugin.git?token=private#person", OriginExternal, "example.com"},
		{"git@example.com:owner/private-plugin.git", OriginExternal, "example.com"},
		{"file:///Users/private-user/src/plugin", OriginExternal, OriginLocalDirectory},
		{"/Users/private-user/src/plugin", OriginExternal, OriginLocalDirectory},
		{`C:\Users\private-user\src\plugin`, OriginExternal, OriginLocalDirectory},
		{"./relative", OriginExternal, OriginLocalDirectory},
	}
	for _, tc := range cases {
		origin, reported := PluginOrigin(tc.source)
		if origin != tc.origin || reported != tc.reported {
			t.Errorf("PluginOrigin(%q) = %q %q, want %q %q",
				tc.source, origin, reported, tc.origin, tc.reported)
		}
	}
}

func TestListSupportPluginsPreservesBrokenManifestsWithoutContent(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "private-owner-alpha-plugin")
	invalid := filepath.Join(root, "private-owner-beta-plugin")
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, plugininstall.ManifestFilename), []byte(`{"name":"SyntheticPrivatePlugin"`), 0o600); err != nil {
		t.Fatal(err)
	}

	plugins, inventory := listSupportPlugins(root)
	if inventory != PluginInventoryReadable {
		t.Fatalf("plugin inventory = %q", inventory)
	}
	if len(plugins) != 2 {
		t.Fatalf("got %d plugin entries, want 2", len(plugins))
	}
	statuses := map[string]bool{}
	for _, item := range plugins {
		if item.Name != "unknown" {
			t.Errorf("broken manifest exposed name %q", item.Name)
		}
		statuses[item.ManifestStatus] = true
	}
	if !statuses[ManifestMissing] || !statuses[ManifestInvalid] {
		t.Fatalf("manifest statuses = %#v", statuses)
	}
	rendered := Render(Snapshot{Plugins: plugins, Features: map[string]bool{}})
	for _, private := range []string{"private-owner-alpha", "private-owner-beta", "SyntheticPrivatePlugin"} {
		if strings.Contains(rendered, private) {
			t.Errorf("rendered report leaked %q", private)
		}
	}
}

func TestListSupportPluginsPreservesUnreadableInventory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	plugins, inventory := listSupportPlugins(root)
	if inventory != PluginInventoryUnreadable || len(plugins) != 0 {
		t.Fatalf("plugin inventory = %q, plugins = %#v", inventory, plugins)
	}
	rendered := Render(Snapshot{PluginInventory: inventory, Features: map[string]bool{}})
	if !strings.Contains(rendered, "PLUGINS\ninventory: unreadable") || strings.Contains(rendered, "PLUGINS\nnone") {
		t.Fatalf("unreadable inventory was omitted:\n%s", rendered)
	}
}

func TestRenderEscapesDynamicText(t *testing.T) {
	poison := "value\n```\nprivate"
	rendered := Render(Snapshot{
		GeneratedAt: poison,
		Identity: Identity{
			Version: poison, Commit: poison, OS: poison, Arch: poison,
			InstallLayout: poison, BinaryShape: poison,
		},
		Plugins: []Plugin{{
			Name: "external", Version: poison, Origin: OriginExternal,
			Source: "example.com", Checksum: "sum\rvalue", ManifestStatus: ManifestOK,
		}},
		Federation: Federation{
			Mode: poison, Serving: poison, CorpusCustody: poison,
			Stores:     []Store{{Name: poison, Readable: true, Families: map[string]int{poison: 1}}},
			Migrations: []Migration{{Plugin: poison, Name: poison, State: poison}},
		},
		Health: []service.HealthVerdict{{Name: poison, Status: poison}},
		Vector: &Vector{
			Model: poison, Chunks: map[string]int{poison: 1},
			LastDelta: &Delta{FinishedAt: poison},
		},
		Ingest:   Ingest{DetectedAgents: []string{poison}, LastIngestAt: poison},
		Features: map[string]bool{},
	})
	if strings.Count(rendered, "```") != 2 {
		t.Fatalf("plugin metadata broke report fence:\n%s", rendered)
	}
	if strings.Contains(rendered, "\nprivate") || strings.Contains(rendered, "\rvalue") {
		t.Fatalf("plugin metadata introduced control characters:\n%s", rendered)
	}
	if !strings.Contains(rendered, `value\n\u0060\u0060\u0060\nprivate`) {
		t.Fatalf("dynamic text was not escaped:\n%s", rendered)
	}
}
