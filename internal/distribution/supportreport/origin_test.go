package supportreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
)

func TestPluginOriginRedactsFilesystemSources(t *testing.T) {
	cases := []struct {
		source, origin, reported string
	}{
		{"bundled:roca", OriginBundled, "bundled:roca"},
		{"owner/repo", OriginExternal, OriginRemote},
		{"https://example.com/plugin.git", OriginExternal, "example.com"},
		{"https://javier:secret@example.com/plugin.git?token=private#person", OriginExternal, "example.com"},
		{"file:///Users/javiermellado/src/plugin", OriginExternal, OriginLocalDirectory},
		{"/Users/javiermellado/src/plugin", OriginExternal, OriginLocalDirectory},
		{`C:\Users\javiermellado\src\plugin`, OriginExternal, OriginLocalDirectory},
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
	missing := filepath.Join(root, "Javier-private-plugin")
	invalid := filepath.Join(root, "Ana-private-plugin")
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, plugininstall.ManifestFilename), []byte(`{"name":"Nortada"`), 0o600); err != nil {
		t.Fatal(err)
	}

	plugins := listSupportPlugins(root)
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
	for _, private := range []string{"Javier", "Ana", "Nortada"} {
		if strings.Contains(rendered, private) {
			t.Errorf("rendered report leaked %q", private)
		}
	}
}

func TestRenderEscapesPluginMetadata(t *testing.T) {
	rendered := Render(Snapshot{
		Plugins: []Plugin{{
			Name: "external", Version: "1\n```\nprivate", Origin: OriginExternal,
			Source: "example.com", Checksum: "sum\rvalue", ManifestStatus: ManifestOK,
		}},
		Features: map[string]bool{},
	})
	if strings.Count(rendered, "```") != 2 {
		t.Fatalf("plugin metadata broke report fence:\n%s", rendered)
	}
	if strings.Contains(rendered, "\nprivate") || strings.Contains(rendered, "\rvalue") {
		t.Fatalf("plugin metadata introduced control characters:\n%s", rendered)
	}
	if !strings.Contains(rendered, `1\n\u0060\u0060\u0060\nprivate`) {
		t.Fatalf("plugin metadata was not escaped:\n%s", rendered)
	}
}
