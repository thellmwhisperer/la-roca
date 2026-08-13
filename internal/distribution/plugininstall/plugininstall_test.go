package plugininstall_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

func TestInspectVerifiesTheSourceAndClassifiesItsRisk(t *testing.T) {
	for _, executable := range []bool{false, true} {
		t.Run(fmt.Sprintf("executable=%t", executable), func(t *testing.T) {
			source := writePackage(t, "synthetic", "1.2.3", false, executable)
			candidate, err := plugininstall.Inspect(source, source)
			if err != nil {
				t.Fatal(err)
			}
			want := plugininstall.DataOnly
			if executable {
				want = plugininstall.Executable
			}
			if candidate.Risk != want || candidate.Checksum == "" || candidate.Version != "1.2.3" {
				t.Fatalf("candidate = %+v", candidate)
			}

			semantic := filepath.Join(source, "semantic.yaml")
			if err := os.WriteFile(semantic, []byte("changed after publication\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := plugininstall.Inspect(source, source); err == nil ||
				!strings.Contains(err.Error(), "checksum") {
				t.Fatalf("tampered source passed with %v", err)
			}
		})
	}
}

func TestInstallUpdateAndUninstallPreservePluginOwnedData(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	source := writePackage(t, "synthetic", "1.0.0", false, true)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}

	installedDB := filepath.Join(root, "synthetic", "plugin.db")
	db, err := sql.Open("sqlite", installedDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO records (value) VALUES ('user-owned update marker')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	writePackageAt(t, source, "synthetic", "2.0.0", false, true)
	updated, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(updated); err != nil {
		t.Fatal(err)
	}
	assertDatabaseValue(t, installedDB, "user-owned update marker")

	manifest, err := plugininstall.ReadManifest(filepath.Join(root, "synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Source != source || manifest.Version != "2.0.0" || manifest.Checksum != updated.Checksum {
		t.Fatalf("manifest = %+v", manifest)
	}

	if _, err := manager.Uninstall("synthetic"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "synthetic"), filepath.Join(bin, "roca-synthetic")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("uninstall kept %s: %v", path, err)
		}
	}
}

func TestCustodialUninstallArchivesTheWholePlugin(t *testing.T) {
	base := t.TempDir()
	manager := plugininstall.Manager{
		PluginRoot:  filepath.Join(base, "plugins"),
		BinDir:      filepath.Join(base, "bin"),
		ArchiveRoot: filepath.Join(base, "plugin-custody"),
		Now:         func() time.Time { return time.Date(2026, 8, 13, 21, 5, 0, 0, time.UTC) },
	}
	source := writePackage(t, "custodial", "1.0.0", true, false)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Uninstall("custodial")
	if err != nil {
		t.Fatal(err)
	}
	wantArchive := filepath.Join(base, "plugin-custody", "custodial-20260813T210500Z")
	if result.ArchivedTo != wantArchive {
		t.Fatalf("archive = %q, want %q", result.ArchivedTo, wantArchive)
	}
	if _, err := os.Stat(filepath.Join(manager.PluginRoot, "custodial")); !os.IsNotExist(err) {
		t.Fatalf("custodial source directory remains: %v", err)
	}
	assertDatabaseValue(t, filepath.Join(wantArchive, "plugin.db"), "source marker")
}

func TestResolveAcceptsPathsAndNormalizesRepositoryNames(t *testing.T) {
	source := writePackage(t, "synthetic", "1.0.0", false, false)
	resolved, cleanup, err := plugininstall.Resolve(context.Background(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if resolved.Reference != source || resolved.Directory != source {
		t.Fatalf("resolved path = %+v", resolved)
	}
	if got, ok := plugininstall.RepositoryURL("private-owner/private-plugin"); !ok ||
		got != "https://github.com/private-owner/private-plugin.git" {
		t.Fatalf("repository URL = %q, %v", got, ok)
	}
}

func writePackage(t *testing.T, name, version string, custody, executable bool) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	writePackageAt(t, directory, name, version, custody, executable)
	return directory
}

func writePackageAt(t *testing.T, directory, name, version string, custody, executable bool) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"schema": 1, "name": name, "version": version})
	if err != nil {
		t.Fatal(err)
	}
	semantic := fmt.Sprintf("version: 1\ndescription: Synthetic records.\ncustody: %t\nquestions:\n  - Which synthetic records exist?\ntables:\n  - name: records\n    description: Synthetic records only.\n    columns: [id, value]\n", custody)
	writeFixtureFile(t, filepath.Join(directory, "plugin.json"), append(metadata, '\n'), 0o600)
	writeFixtureFile(t, filepath.Join(directory, "semantic.yaml"), []byte(semantic), 0o600)
	if err := os.Remove(filepath.Join(directory, "plugin.db")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "plugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE records (id INTEGER PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO records (value) VALUES ('source marker')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(directory, "roca-"+name)
	if executable {
		writeFixtureFile(t, executablePath, []byte("#!/bin/sh\nprintf 'synthetic plugin\\n'\n"), 0o700)
	} else if err := os.Remove(executablePath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	files := []string{"plugin.json", "semantic.yaml", "plugin.db"}
	if executable {
		files = append(files, "roca-"+name)
	}
	var checksums strings.Builder
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(directory, file))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(raw)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(digest[:]), file)
	}
	writeFixtureFile(t, filepath.Join(directory, "checksums.txt"), []byte(checksums.String()), 0o600)
}

func writeFixtureFile(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func assertDatabaseValue(t *testing.T, path, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM records WHERE value = ?`, want).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("database %s has %d copies of %q", path, count, want)
	}
}
