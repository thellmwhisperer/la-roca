package bundledplugin_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
)

func TestEnsureAllPreflightsEveryBundleBeforeUpdatingAny(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	alphaV1 := executableSpec("alpha", []byte("alpha one"))
	if _, err := bundledplugin.Ensure(root, bin, "v1", alphaV1); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, executableName("beta")), []byte("locally owned"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := bundledplugin.EnsureAll(root, bin, "v2",
		executableSpec("alpha", []byte("alpha two")),
		executableSpec("beta", []byte("beta two")))
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite existing executable") {
		t.Fatalf("batch preflight error = %v", err)
	}
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v1" {
		t.Fatalf("alpha version = %q, want v1", manifest.Version)
	}
	raw, err := os.ReadFile(filepath.Join(bin, executableName("alpha")))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "alpha one" {
		t.Fatalf("alpha executable = %q, want original payload", raw)
	}
}

func TestEnsureAllPreflightsInstalledSchemasBeforeUpdatingAny(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	alpha, beta := dataSpec("alpha"), dataSpec("beta")
	for _, spec := range []bundledplugin.Spec{alpha, beta} {
		if _, err := bundledplugin.Ensure(root, bin, "v1", spec); err != nil {
			t.Fatal(err)
		}
	}
	betaDatabase := filepath.Join(root, "beta", "beta.db")
	db, err := bundledplugin.OpenDatabase(betaDatabase, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE plugin_schema SET schema_version = 2"); err != nil {
		t.Fatal(err)
	}

	_, err = bundledplugin.EnsureAll(root, bin, "v2", alpha, beta)
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("schema preflight error = %v", err)
	}
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "v1" {
		t.Fatalf("alpha version = %q, want v1", manifest.Version)
	}
}

func executableSpec(name string, payload []byte) bundledplugin.Spec {
	return bundledplugin.Spec{
		Name: name, Executable: executableName(name), Source: "bundled:roca",
		Manifest: []byte(`{"schema":1,"kind":"executable","state_directory":"state"}`),
		Payload:  func() ([]byte, error) { return payload, nil },
	}
}

func dataSpec(name string) bundledplugin.Spec {
	return bundledplugin.Spec{
		Name: name, DatabaseFilename: name + ".db", Source: "bundled:roca",
		Semantic: []byte("version: 1\nattachment: on-demand\n" +
			"description: Synthetic data bundle.\nquestions:\n  - Which rows exist?\n" +
			"tables:\n  - name: records\n    description: Synthetic rows.\n    columns: [id]\n"),
		ApplySchema: func(path string) error {
			return bundledplugin.ApplySchema(path, name,
				`CREATE TABLE IF NOT EXISTS records (id INTEGER PRIMARY KEY)`, 1, 0)
		},
	}
}

func executableName(name string) string {
	executable := "roca-" + name
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	return executable
}
