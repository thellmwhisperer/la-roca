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
	"slices"
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

func TestInspectAcceptsAndVerifiesAnOptionalRideManifest(t *testing.T) {
	source := writePackage(t, "synthetic", "1.2.3", false, false)
	rides := []byte("[ride.ingest]\ncommand = \"roca synthetic ingest\"\n")
	addRideManifest(t, source, rides)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Files["rides.toml"] == "" {
		t.Fatalf("candidate does not own rides.toml: %+v", candidate.Files)
	}
	if candidate.Risk != plugininstall.Executable {
		t.Fatalf("a package whose rides run shell commands is classified %q", candidate.Risk)
	}
	if err := os.WriteFile(filepath.Join(source, "rides.toml"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugininstall.Inspect(source, source); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered rides passed with %v", err)
	}

	unresolvable := writePackage(t, "synthetic", "1.2.3", false, false)
	addRideManifest(t, unresolvable,
		[]byte("[ride.prune]\ncommand = \"roca synthetic prune\"\ngate = \"after_compact\"\n"))
	if _, err := plugininstall.Inspect(unresolvable, unresolvable); err == nil ||
		!strings.Contains(err.Error(), "after_compact") {
		t.Fatalf("an unresolvable gate passed with %v", err)
	}
}

func TestVerifyInstalledPayloadRejectsTamperedRidesAndManifestIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	bin := filepath.Join(t.TempDir(), "bin")
	source := writePackage(t, "synthetic", "1.2.3", false, false)
	addRideManifest(t, source, []byte("[ride.delta]\ncommand = \"roca synthetic delta\"\n"))
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (plugininstall.Manager{PluginRoot: root, BinDir: bin}).Install(candidate); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(root, "synthetic")
	if _, err := plugininstall.VerifyInstalledPayload("synthetic", installed); err != nil {
		t.Fatalf("verified install was rejected: %v", err)
	}

	if err := os.WriteFile(filepath.Join(installed, "rides.toml"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugininstall.VerifyInstalledPayload("synthetic", installed); err == nil ||
		!strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered rides passed with %v", err)
	}

	if err := os.WriteFile(filepath.Join(installed, "rides.toml"),
		[]byte("[ride.delta]\ncommand = \"roca synthetic delta\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plugininstall.VerifyInstalledPayload("different", installed); err == nil ||
		!strings.Contains(err.Error(), "different") {
		t.Fatalf("mismatched manifest identity passed with %v", err)
	}
}

func addRideManifest(t *testing.T, source string, rides []byte) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(source, "rides.toml"), rides, 0o600)
	digest := sha256.Sum256(rides)
	checksums := filepath.Join(source, "checksums.txt")
	file, err := os.OpenFile(checksums, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(file, "%s  rides.toml\n", hex.EncodeToString(digest[:])); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesASourceFileSwappedForASymlinkAfterInspection(t *testing.T) {
	source := writePackage(t, "synthetic", "1.2.3", false, false)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}

	semantic := filepath.Join(source, "semantic.yaml")
	published, err := os.ReadFile(semantic)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "semantic.yaml")
	if err := os.WriteFile(external, published, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(semantic); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, semantic); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	manager := plugininstall.Manager{
		PluginRoot: filepath.Join(t.TempDir(), "plugins"),
		BinDir:     filepath.Join(t.TempDir(), "bin"),
	}
	_, err = manager.Install(candidate)
	if err == nil {
		t.Fatal("install accepted a checksummed source file swapped for a symlink")
	}
	if !strings.Contains(err.Error(), "checksum source file semantic.yaml is not a regular file") {
		t.Fatalf("err = %v, want the contractual non-regular source refusal", err)
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

	recovery := filepath.Join(root, ".synthetic.previous")
	if err := os.Mkdir(recovery, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(updated); err == nil || !strings.Contains(err.Error(), recovery) {
		t.Fatalf("update recovery directory is not hidden from discovery: %v", err)
	}
	if err := os.RemoveAll(recovery); err != nil {
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

func TestExecutableOnlyPackageOwnsAndPreservesItsStateDirectory(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	source := writeExecutablePackage(t, "synthetic-exec", "1.0.0", "state")
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Kind != plugininstall.ExecutablePackage || candidate.Database != "" ||
		candidate.StateDir != "state" || candidate.Risk != plugininstall.Executable {
		t.Fatalf("candidate = %+v", candidate)
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(root, "synthetic-exec", "state", "index.db")
	writeFixtureFile(t, stateFile, []byte("derived index"), 0o600)

	writeExecutablePackageAt(t, source, "synthetic-exec", "2.0.0", "state")
	updated, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(updated); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(stateFile); err != nil || string(raw) != "derived index" {
		t.Fatalf("preserved state = %q, err=%v", raw, err)
	}

	if err := os.RemoveAll(filepath.Join(root, "synthetic-exec", "state")); err != nil {
		t.Fatal(err)
	}
	writeExecutablePackageAt(t, source, "synthetic-exec", "3.0.0", "state")
	rebuilt, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(rebuilt); err != nil {
		t.Fatalf("update after the operator reclaimed the rebuildable state: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "synthetic-exec", "state")); err != nil || !info.IsDir() {
		t.Fatalf("state directory was not recreated: %v, err=%v", info, err)
	}
	writeFixtureFile(t, stateFile, []byte("derived index"), 0o600)

	manifest, err := plugininstall.ReadManifest(filepath.Join(root, "synthetic-exec"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(plugininstall.InstalledPaths(filepath.Join(root, "synthetic-exec"), manifest), stateFile) {
		t.Fatalf("manifest inventory does not own %s", stateFile)
	}
	if _, err := manager.Uninstall("synthetic-exec"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("uninstall kept executable state: %v", err)
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
	writePackageMetadata(t, directory, map[string]any{"schema": 1, "name": name, "version": version})
	semantic := fmt.Sprintf("version: 1\ndescription: Synthetic records.\ncustody: %t\nquestions:\n  - Which synthetic records exist?\ntables:\n  - name: records\n    description: Synthetic records only.\n    columns: [id, value]\n", custody)
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
	writeChecksums(t, directory, files)
}

func writeExecutablePackage(t *testing.T, name, version, stateDir string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	writeExecutablePackageAt(t, directory, name, version, stateDir)
	return directory
}

func writeExecutablePackageAt(t *testing.T, directory, name, version, stateDir string) {
	t.Helper()
	writePackageMetadata(t, directory, map[string]any{
		"schema": 1, "name": name, "version": version,
		"kind": "executable", "state_directory": stateDir,
	})
	executable := "roca-" + name
	writeFixtureFile(t, filepath.Join(directory, executable), []byte("#!/bin/sh\nexit 0\n"), 0o700)
	writeChecksums(t, directory, []string{"plugin.json", executable})
}

func writePackageMetadata(t *testing.T, directory string, fields map[string]any) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(directory, "plugin.json"), append(metadata, '\n'), 0o600)
}

func writeChecksums(t *testing.T, directory string, files []string) {
	t.Helper()
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
