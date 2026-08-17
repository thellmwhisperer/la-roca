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

	assertEnsureAllRejected(t, root, bin, "batch preflight", "refusing to overwrite existing executable",
		executableSpec("alpha", []byte("alpha two")),
		executableSpec("beta", []byte("beta two")))
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

	assertEnsureAllRejected(t, root, bin, "schema preflight", "newer than supported", alpha, beta)
}

func TestEnsureAllRejectsAReadOnlyDatabaseBeforeUpdatingAny(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix file modes")
	}
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	alpha, beta := dataSpec("alpha"), dataSpec("beta")
	installDataSpecs(t, root, bin, "v1", alpha, beta)
	betaDatabase := filepath.Join(root, "beta", "beta.db")
	if err := os.Chmod(betaDatabase, 0o400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(betaDatabase, 0o600)

	if _, err := bundledplugin.EnsureAll(root, bin, "v2", alpha, beta); err == nil {
		t.Fatal("read-only database passed the bundled preflight")
	}
	assertManifestVersion(t, root, "alpha", "v1")
}

func TestEnsureAllConvergesMixedVersionsOnNextStartup(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	alpha, beta := dataSpec("alpha"), dataSpec("beta")
	installDataSpecs(t, root, bin, "v1", alpha, beta)
	if _, err := bundledplugin.Ensure(root, bin, "v2", alpha); err != nil {
		t.Fatal(err)
	}
	assertManifestVersion(t, root, "alpha", "v2")
	assertManifestVersion(t, root, "beta", "v1")

	if _, err := bundledplugin.EnsureAll(root, bin, "v2", alpha, beta); err != nil {
		t.Fatal(err)
	}
	assertManifestVersion(t, root, "alpha", "v2")
	assertManifestVersion(t, root, "beta", "v2")
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

func installDataSpecs(t *testing.T, root, bin, version string, specs ...bundledplugin.Spec) {
	t.Helper()
	for _, spec := range specs {
		if _, err := bundledplugin.Ensure(root, bin, version, spec); err != nil {
			t.Fatal(err)
		}
	}
}

func assertEnsureAllRejected(
	t *testing.T,
	root, bin, label, wantError string,
	specs ...bundledplugin.Spec,
) {
	t.Helper()
	_, err := bundledplugin.EnsureAll(root, bin, "v2", specs...)
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("%s error = %v", label, err)
	}
	assertManifestVersion(t, root, "alpha", "v1")
}

func assertManifestVersion(t *testing.T, root, name, version string) {
	t.Helper()
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != version {
		t.Fatalf("%s version = %q, want %q", name, manifest.Version, version)
	}
}

func executableName(name string) string {
	executable := "roca-" + name
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	return executable
}
