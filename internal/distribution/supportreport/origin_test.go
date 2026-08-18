package supportreport

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	_ "modernc.org/sqlite"
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
	mismatched := filepath.Join(root, "expected-plugin")
	linked := filepath.Join(root, "linked-plugin")
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mismatched, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, plugininstall.ManifestFilename), []byte(`{"name":"SyntheticPrivatePlugin"`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSupportManifest(t, filepath.Join(mismatched, plugininstall.ManifestFilename), "copied-plugin")
	externalManifest := filepath.Join(t.TempDir(), "external-manifest.json")
	writeSupportManifest(t, externalManifest, "linked-plugin")
	if err := os.Symlink(externalManifest, filepath.Join(linked, plugininstall.ManifestFilename)); err != nil {
		t.Fatal(err)
	}

	plugins, inventory := listSupportPlugins(root)
	if inventory != PluginInventoryReadable {
		t.Fatalf("plugin inventory = %q", inventory)
	}
	if len(plugins) != 4 {
		t.Fatalf("got %d plugin entries, want 4", len(plugins))
	}
	statuses := map[string]int{}
	for _, item := range plugins {
		if item.Name != "unknown" {
			t.Errorf("broken manifest exposed name %q", item.Name)
		}
		statuses[item.ManifestStatus]++
	}
	if statuses[ManifestMissing] != 1 || statuses[ManifestInvalid] != 3 {
		t.Fatalf("manifest statuses = %#v", statuses)
	}
	rendered := Render(Snapshot{Plugins: plugins, Features: map[string]bool{}})
	for _, private := range []string{"private-owner-alpha", "private-owner-beta", "SyntheticPrivatePlugin"} {
		if strings.Contains(rendered, private) {
			t.Errorf("rendered report leaked %q", private)
		}
	}
}

func writeSupportManifest(t *testing.T, path, name string) {
	t.Helper()
	raw, err := json.Marshal(plugininstall.Manifest{
		Schema: 1, Name: name, Source: "bundled:roca", Version: "1.0.0",
		Checksum: "synthetic-checksum", Kind: plugininstall.DataPackage,
		Database: "plugin.db", Files: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
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

func TestOpenSupportStoreReportsDanglingSymlinkAsUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roca.db")
	if err := os.Symlink("missing.db", path); err != nil {
		t.Fatal(err)
	}
	store, closeStore := openSupportStore(path)
	defer closeStore()
	if !store.present || store.db != nil {
		t.Fatalf("dangling store = %+v, want present and unreadable", store)
	}

	absent, closeAbsent := openSupportStore(filepath.Join(t.TempDir(), "absent.db"))
	defer closeAbsent()
	if absent.present || absent.db != nil {
		t.Fatalf("absent store = %+v", absent)
	}
}

func TestLastIngestAtCanonicalizesAndRedactsInvalidValues(t *testing.T) {
	db := openSupportReportDatabase(t)
	if _, err := db.Exec(`
		CREATE TABLE ingest_file_state (last_synced_at TEXT);
		INSERT INTO ingest_file_state VALUES
			('2026-08-18 11:00:00'),
			('2026-08-18T10:30:00-02:00');`); err != nil {
		t.Fatal(err)
	}
	if got := lastIngestAt(t.Context(), db); got != "2026-08-18T12:30:00Z" {
		t.Fatalf("last ingest = %q", got)
	}
	private := "/Users/private-user secret conversation"
	if _, err := db.Exec(`INSERT INTO ingest_file_state VALUES (?)`, private); err != nil {
		t.Fatal(err)
	}
	got := lastIngestAt(t.Context(), db)
	if got != "invalid" {
		t.Fatalf("invalid last ingest = %q", got)
	}
	raw, err := json.Marshal(Snapshot{Ingest: Ingest{LastIngestAt: got}})
	if err != nil {
		t.Fatal(err)
	}
	rendered := Render(Snapshot{Ingest: Ingest{LastIngestAt: got}, Features: map[string]bool{}})
	if strings.Contains(string(raw), private) || strings.Contains(rendered, private) {
		t.Fatal("invalid timestamp content leaked into the report")
	}
}

func openSupportReportDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "support.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
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
