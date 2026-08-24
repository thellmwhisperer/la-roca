package plugininstall_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	_ "modernc.org/sqlite"
)

func TestExecutableNameKeepsAFamilyPrefix(t *testing.T) {
	for _, test := range []struct {
		name, want string
	}{
		{name: "vector", want: "roca-vector"},
		{name: "roca-vector", want: "roca-vector"},
		{name: "roca-ops", want: "roca-ops"},
		{name: "synthetic", want: "roca-synthetic"},
	} {
		if got := plugininstall.ExecutableName(test.name); got != test.want {
			t.Fatalf("ExecutableName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestInspectAcceptsAnOptionalCompanionDeclaration(t *testing.T) {
	source := t.TempDir()
	writePackageMetadata(t, source, map[string]any{
		"schema": 1, "name": "synthetic-exec", "version": "1.0.0",
		"kind":      "executable",
		"companion": map[string]any{"executable": "roca-synthetic-exec", "args": []string{"watch"}},
	})
	executable := plugininstall.ExecutableName("synthetic-exec")
	writeFixtureFile(t, filepath.Join(source, executable), []byte("#!/bin/sh\nexit 0\n"), 0o700)
	writeChecksums(t, source, []string{"plugin.json", executable})
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Name != "synthetic-exec" || candidate.Kind != plugininstall.ExecutablePackage {
		t.Fatalf("candidate = %+v", candidate)
	}

	unknown := t.TempDir()
	writePackageMetadata(t, unknown, map[string]any{
		"schema": 1, "name": "synthetic-exec", "version": "1.0.0",
		"kind": "executable", "mystery": true,
	})
	writeFixtureFile(t, filepath.Join(unknown, executable), []byte("#!/bin/sh\nexit 0\n"), 0o700)
	writeChecksums(t, unknown, []string{"plugin.json", executable})
	if _, err := plugininstall.Inspect(unknown, unknown); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
}

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
	installedSidecar := filepath.Join(root, "synthetic", "plugin.vector.db")
	writeFixtureFile(t, installedSidecar, []byte("derived vectors"), 0o600)
	writeFixtureFile(t, installedSidecar+"-wal", []byte("derived wal"), 0o600)

	writePackageAt(t, source, "synthetic", "2.0.0", false, true)
	updated, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}

	for _, recovery := range []string{
		filepath.Join(root, ".synthetic.previous"),
		filepath.Join(root, ".synthetic.recovery"),
		filepath.Join(root, ".synthetic.recovery.journal"),
	} {
		if err := os.Mkdir(recovery, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Update(updated); err == nil || !strings.Contains(err.Error(), recovery) {
			t.Fatalf("update recovery directory %s was ignored: %v", recovery, err)
		}
		if err := os.RemoveAll(recovery); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := manager.Update(updated); err != nil {
		t.Fatal(err)
	}
	assertDatabaseValue(t, installedDB, "user-owned update marker")
	for path, want := range map[string]string{
		installedSidecar:          "derived vectors",
		installedSidecar + "-wal": "derived wal",
	} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("preserved sidecar %s = %q, err=%v", filepath.Base(path), raw, err)
		}
	}

	manifest, err := plugininstall.ReadManifest(filepath.Join(root, "synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Source != source || manifest.Version != "2.0.0" || manifest.Checksum != updated.Checksum {
		t.Fatalf("manifest = %+v", manifest)
	}
	if paths := plugininstall.InstalledPaths(filepath.Join(root, "synthetic"), manifest); !slices.Contains(paths, installedSidecar) || !slices.Contains(paths, installedSidecar+"-wal") {
		t.Fatalf("installed ownership omits vector sidecar: %v", paths)
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

func TestRecoverUpdateRestoresTransferredVectorSidecars(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	source := writePackage(t, "synthetic", "1.0.0", false, false)
	previous, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(previous); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "synthetic")
	backup := filepath.Join(root, ".synthetic.previous")
	database := filepath.Join(target, "plugin.db")
	withPackageDatabase(t, database, func(db *sql.DB) {
		if _, err := db.Exec(`INSERT INTO records (value) VALUES ('previous database')`); err != nil {
			t.Fatal(err)
		}
	})
	sidecar := filepath.Join(target, "plugin.vector.db")
	writeFixtureFile(t, sidecar, []byte("previous vectors"), 0o600)
	writeFixtureFile(t, sidecar+"-wal", []byte("previous vector wal"), 0o600)

	if err := os.Rename(target, backup); err != nil {
		t.Fatal(err)
	}
	writePackageAt(t, source, "synthetic", "2.0.0", false, false)
	current, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(current); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		if err := os.Rename(filepath.Join(backup, "plugin.vector.db"+suffix),
			filepath.Join(target, "plugin.vector.db"+suffix)); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.RecoverUpdate("synthetic"); err != nil {
		t.Fatal(err)
	}
	assertDatabaseValue(t, database, "previous database")
	for path, want := range map[string]string{
		sidecar: "previous vectors", sidecar + "-wal": "previous vector wal",
	} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("recovered sidecar %s = %q, err=%v", filepath.Base(path), raw, err)
		}
	}
}

func TestInterruptedUpdateRecoveryTombstoneConverges(t *testing.T) {
	for _, testCase := range []struct {
		name, wantError, preserved, wantContents string
		arrange                                  func(*testing.T, string, string, string, string, string)
	}{
		{
			name: "proof created before journal",
			arrange: func(t *testing.T, target, backup, _, _, proof string) {
				t.Helper()
				if err := os.Rename(target, backup); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{
					plugininstall.PackageFilename,
					plugininstall.ChecksumsFilename,
					plugininstall.ManifestFilename,
					plugininstall.ExecutableName("synthetic-exec"),
				} {
					raw, err := os.ReadFile(filepath.Join(backup, name))
					if err != nil {
						t.Fatal(err)
					}
					mode := os.FileMode(0o600)
					if name == plugininstall.ExecutableName("synthetic-exec") {
						mode = 0o700
					}
					writeFixtureFile(t, filepath.Join(target, name), raw, mode)
				}
				writeFixtureFile(t, filepath.Join(target, ".roca-update-recovery"), []byte(proof), 0o600)
			},
		},
		{
			name: "target tombstoned before previous restore",
			arrange: func(t *testing.T, target, backup, tombstone, journal, proof string) {
				t.Helper()
				if err := os.Rename(target, backup); err != nil {
					t.Fatal(err)
				}
				writeRecoveryFixture(t, tombstone, proof, true, "partial", "discarded update")
				linkRecoveryJournal(t, tombstone, journal)
			},
		},
		{
			name: "previous restored before tombstone cleanup",
			arrange: func(t *testing.T, _, _, tombstone, journal, proof string) {
				writeRecoveryFixture(t, tombstone, proof, true, "partial", "discarded update")
				linkRecoveryJournal(t, tombstone, journal)
			},
		},
		{
			name:         "nonempty tombstone without proof is preserved",
			wantError:    "has no linked ownership proof and is not empty",
			preserved:    "partial",
			wantContents: "discarded update",
			arrange: func(t *testing.T, _, _, tombstone, journal, proof string) {
				writeRecoveryFixture(t, tombstone, proof, false, "partial", "discarded update")
				writeFixtureFile(t, journal, []byte(proof), 0o600)
			},
		},
		{
			name: "empty tombstone after proof removal converges",
			arrange: func(t *testing.T, _, _, tombstone, journal, proof string) {
				writeRecoveryFixture(t, tombstone, proof, false)
				writeFixtureFile(t, journal, []byte(proof), 0o600)
			},
		},
		{
			name:         "unowned tombstone collision is preserved",
			wantError:    "has no installer ownership journal",
			preserved:    "unowned",
			wantContents: "operator data",
			arrange: func(t *testing.T, _, _, tombstone, _, _ string) {
				writeRecoveryFixture(t, tombstone, "", false, "unowned", "operator data")
			},
		},
		{
			name:         "unlinked ownership files are preserved",
			wantError:    "is not linked to installer journal",
			preserved:    "unowned",
			wantContents: "operator data",
			arrange: func(t *testing.T, _, _, tombstone, journal, proof string) {
				writeRecoveryFixture(t, tombstone, proof, true, "unowned", "operator data")
				writeFixtureFile(t, journal, []byte(proof), 0o600)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
			manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
			_, candidate := inspectExecutablePackage(t, "synthetic-exec", "1.0.0", "state")
			if _, err := manager.Install(candidate); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "synthetic-exec")
			backup := filepath.Join(root, ".synthetic-exec.previous")
			tombstone := filepath.Join(root, ".synthetic-exec.recovery")
			state := filepath.Join(target, "state", "index.db")
			writeFixtureFile(t, state, []byte("preserved index"), 0o600)
			manifest, err := plugininstall.ReadManifest(target)
			if err != nil {
				t.Fatal(err)
			}
			proof := fmt.Sprintf("roca-plugin-update-recovery-v1\nsynthetic-exec\n%s\n", manifest.Checksum)
			before, err := os.Lstat(state)
			if err != nil {
				t.Fatal(err)
			}

			journal := tombstone + ".journal"
			testCase.arrange(t, target, backup, tombstone, journal, proof)
			err = manager.RecoverUpdate("synthetic-exec")
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("RecoverUpdate() error = %v, want %q", err, testCase.wantError)
				}
				raw, readErr := os.ReadFile(filepath.Join(tombstone, testCase.preserved))
				if readErr != nil || string(raw) != testCase.wantContents {
					t.Fatalf("preserved tombstone content = %q, err=%v", raw, readErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.RecoverUpdate("synthetic-exec"); err != nil {
				t.Fatal(err)
			}

			after, err := os.Lstat(state)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Size() != after.Size() {
				t.Fatalf("state identity changed: same=%v size %d -> %d",
					os.SameFile(before, after), before.Size(), after.Size())
			}
			for _, path := range []string{backup, tombstone, journal} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("recovery artifact remains at %s: %v", path, err)
				}
			}
		})
	}
}

func TestFederatedManifestInstallsAndPreservesEveryDeclaredDatabase(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	source := filepath.Join(t.TempDir(), "federated")
	writeFederatedPackage(t, source, "federated", "1.0.0")
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(candidate.Databases, []string{"records.db", "runs.db"}) {
		t.Fatalf("declared databases = %v", candidate.Databases)
	}
	if _, err := manager.Install(candidate); err != nil {
		t.Fatal(err)
	}
	for _, database := range candidate.Databases {
		withPackageDatabase(t, filepath.Join(root, "federated", database), func(db *sql.DB) {
			if _, err := db.Exec(`INSERT INTO entries (value) VALUES ('preserved')`); err != nil {
				t.Fatal(err)
			}
		})
	}

	writeFederatedPackage(t, source, "federated", "2.0.0")
	updated, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateInPlace(updated); err != nil {
		t.Fatal(err)
	}
	for _, database := range candidate.Databases {
		var count int
		withPackageDatabase(t, filepath.Join(root, "federated", database), func(db *sql.DB) {
			if err := db.QueryRow(`SELECT count(*) FROM entries WHERE value = 'preserved'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
		})
		if count != 1 {
			t.Fatalf("database %s lost its plugin-owned rows", database)
		}
	}
	if _, err := plugininstall.VerifyInstalledPayload("federated", filepath.Join(root, "federated")); err != nil {
		t.Fatal(err)
	}
}

// A package that ships no executable is authorable from the published contract:
// it declares the host binary and stays data-only, while a package that names an
// executable it does not supply is refused.
func TestAFederatedPackageDeclaresEitherTheHostBinaryOrOneItShips(t *testing.T) {
	source := filepath.Join(t.TempDir(), "federated")
	writeFederatedPackage(t, source, "federated", "1.0.0")
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Risk != plugininstall.DataOnly || candidate.Executable != "" {
		t.Fatalf("host-binary package risk = %s, executable = %q", candidate.Risk, candidate.Executable)
	}

	raw, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(source, "plugin.json"),
		[]byte(strings.Replace(string(raw), `"binary":"roca"`, `"binary":"roca-federated"`, 1)), 0o600)
	writeChecksums(t, source, []string{"plugin.json", "records.db", "runs.db"})
	if _, err := plugininstall.Inspect(source, source); err == nil ||
		!strings.Contains(err.Error(), "declares binary") {
		t.Fatalf("package that supplies no declared executable = %v", err)
	}

	docs, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "plugins.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docs), "declares `roca`, the host binary") {
		t.Error("the plugin contract does not document what a package without an executable declares")
	}
}

func TestFederatedManifestNamesMustSurviveTheWholeLifecycle(t *testing.T) {
	source := filepath.Join(t.TempDir(), "package")
	writeFederatedPackage(t, source, "roca.corpus", "1.0.0")
	if _, err := plugininstall.Inspect(source, source); err == nil ||
		!strings.Contains(err.Error(), "safe name") {
		t.Fatalf("inspect of an unmanageable manifest name = %v", err)
	}
}

func TestExecutableOnlyPackageOwnsAndPreservesItsStateDirectory(t *testing.T) {
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	manager := plugininstall.Manager{PluginRoot: root, BinDir: bin}
	source, candidate := inspectExecutablePackage(t, "synthetic-exec", "1.0.0", "state")
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

func TestResolveAcceptsVerifiedReleaseArchivesFromDiskAndHTTP(t *testing.T) {
	source := writeExecutablePackage(t, "roca-vector", "v1.2.3", "state")
	archive := packageArchive(t, source)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(archive)
	}))
	defer server.Close()
	local := filepath.Join(t.TempDir(), "roca-vector-v1.2.3-linux-x64.tar.gz")
	if err := os.WriteFile(local, archive, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, reference := range []string{local, server.URL + "/roca-vector-v1.2.3-linux-x64.tar.gz"} {
		resolved, cleanup, err := plugininstall.Resolve(context.Background(), reference, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		candidate, inspectErr := plugininstall.Inspect(resolved.Reference, resolved.Directory)
		if inspectErr != nil {
			cleanup()
			t.Fatal(inspectErr)
		}
		if candidate.Name != "roca-vector" || candidate.Version != "v1.2.3" ||
			candidate.Source != resolved.Reference {
			cleanup()
			t.Fatalf("candidate from %s = %+v", reference, candidate)
		}
		manager := plugininstall.Manager{
			PluginRoot: filepath.Join(t.TempDir(), "plugins"),
			BinDir:     filepath.Join(t.TempDir(), "bin"),
		}
		result, installErr := manager.Install(candidate)
		cleanup()
		if installErr != nil {
			t.Fatal(installErr)
		}
		if result.Version != "v1.2.3" {
			t.Fatalf("installed result from %s = %+v", reference, result)
		}
		if reference == local {
			nextSource := writeExecutablePackage(t, "roca-vector", "v1.2.4", "state")
			if err := os.WriteFile(local, packageArchive(t, nextSource), 0o600); err != nil {
				t.Fatal(err)
			}
			next, nextCleanup, err := plugininstall.Resolve(context.Background(), local, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			nextCandidate, err := plugininstall.Inspect(next.Reference, next.Directory)
			if err == nil {
				_, err = manager.Update(nextCandidate)
			}
			nextCleanup()
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(manager.PluginRoot, "roca-vector"))
			if err != nil || manifest.Version != "v1.2.4" {
				t.Fatalf("updated manifest = %+v, err=%v", manifest, err)
			}
		}
	}
}

func TestResolveRefusesArchivePathsAndNonRegularEntries(t *testing.T) {
	for _, entry := range []struct {
		name     string
		typeflag byte
	}{
		{name: "../outside", typeflag: tar.TypeReg},
		{name: `..\outside`, typeflag: tar.TypeReg},
		{name: "nested/plugin.json", typeflag: tar.TypeReg},
		{name: "roca-vector", typeflag: tar.TypeSymlink},
	} {
		archive := filepath.Join(t.TempDir(), "plugin.tar.gz")
		if err := os.WriteFile(archive, archiveEntry(t, entry.name, entry.typeflag), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, cleanup, err := plugininstall.Resolve(context.Background(), archive, t.TempDir()); err == nil {
			cleanup()
			t.Fatalf("archive entry %q type %d was accepted", entry.name, entry.typeflag)
		}
	}
}

func TestResolveRefusesArchiveWithTooManyEntries(t *testing.T) {
	archive := makeArchive(t, func(archive *tar.Writer) {
		for index := range 1025 {
			name := fmt.Sprintf("entry-%04d", index)
			if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600}); err != nil {
				t.Fatal(err)
			}
		}
	})
	path := filepath.Join(t.TempDir(), "plugin.tar.gz")
	if err := os.WriteFile(path, archive, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, cleanup, err := plugininstall.Resolve(context.Background(), path, t.TempDir()); err == nil {
		cleanup()
		t.Fatal("archive with too many entries was accepted")
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
	executablePath := filepath.Join(directory, plugininstall.ExecutableName(name))
	if executable {
		writeFixtureFile(t, executablePath, []byte("#!/bin/sh\nprintf 'synthetic plugin\\n'\n"), 0o700)
	} else if err := os.Remove(executablePath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	files := []string{"plugin.json", "semantic.yaml", "plugin.db"}
	if executable {
		files = append(files, plugininstall.ExecutableName(name))
	}
	writeChecksums(t, directory, files)
}

func writeFederatedPackage(t *testing.T, directory, name, version string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema": 1, "name": name, "version": version, "binary": "roca",
		"databases": []map[string]any{
			{"name": "records", "path": "records.db", "alias": "federated_records", "attachment": "resident", "retention": "Plugin managed."},
			{"name": "runs", "path": "runs.db", "alias": "federated_runs", "attachment": "resident", "retention": "Plugin managed."},
		},
		"semantic": map[string]any{
			"databases": []map[string]any{
				{
					"database": "records", "description": "Synthetic records.",
					"questions": []string{"Which records exist?"},
					"tables": []map[string]any{{
						"name": "entries", "description": "Synthetic entries.",
						"columns": []string{"id", "value"},
					}},
				},
				{
					"database": "runs", "description": "Synthetic runs.",
					"questions": []string{"Which runs exist?"},
					"tables": []map[string]any{{
						"name": "entries", "description": "Synthetic run entries.",
						"columns": []string{"id", "value"},
					}},
				},
			},
		},
		"verbs":        []map[string]any{},
		"capabilities": []map[string]any{},
	}
	writePackageMetadata(t, directory, manifest)
	for _, name := range []string{"records.db", "runs.db"} {
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		withPackageDatabase(t, filepath.Join(directory, name), func(db *sql.DB) {
			if _, err := db.Exec(`CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
				t.Fatal(err)
			}
		})
	}
	writeChecksums(t, directory, []string{"plugin.json", "records.db", "runs.db"})
}

func withPackageDatabase(t *testing.T, path string, action func(*sql.DB)) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	action(db)
}

func writeExecutablePackage(t *testing.T, name, version, stateDir string) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), name)
	writeExecutablePackageAt(t, directory, name, version, stateDir)
	return directory
}

func inspectExecutablePackage(t *testing.T, name, version, stateDir string) (string, plugininstall.Candidate) {
	t.Helper()
	source := writeExecutablePackage(t, name, version, stateDir)
	candidate, err := plugininstall.Inspect(source, source)
	if err != nil {
		t.Fatal(err)
	}
	return source, candidate
}

func writeRecoveryFixture(t *testing.T, tombstone, proof string, owned bool, entries ...string) {
	t.Helper()
	if len(entries)%2 != 0 {
		t.Fatal("recovery fixture entries must be name/body pairs")
	}
	if err := os.Mkdir(tombstone, 0o700); err != nil {
		t.Fatal(err)
	}
	if owned {
		writeFixtureFile(t, filepath.Join(tombstone, ".roca-update-recovery"), []byte(proof), 0o600)
	}
	for index := 0; index < len(entries); index += 2 {
		writeFixtureFile(t, filepath.Join(tombstone, entries[index]), []byte(entries[index+1]), 0o600)
	}
}

func linkRecoveryJournal(t *testing.T, tombstone, journal string) {
	t.Helper()
	if err := os.Link(filepath.Join(tombstone, ".roca-update-recovery"), journal); err != nil {
		t.Fatal(err)
	}
}

func writeExecutablePackageAt(t *testing.T, directory, name, version, stateDir string) {
	t.Helper()
	writePackageMetadata(t, directory, map[string]any{
		"schema": 1, "name": name, "version": version,
		"kind": "executable", "state_directory": stateDir,
	})
	executable := plugininstall.ExecutableName(name)
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

func packageArchive(t *testing.T, directory string) []byte {
	t.Helper()
	return makeArchive(t, func(archive *tar.Writer) {
		for _, name := range []string{"checksums.txt", "plugin.json", "roca-vector"} {
			body, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil {
				t.Fatal(err)
			}
			mode := int64(0o600)
			if name == "roca-vector" {
				mode = 0o700
			}
			if err := archive.WriteHeader(&tar.Header{Name: "./" + name, Mode: mode, Size: int64(len(body))}); err != nil {
				t.Fatal(err)
			}
			if _, err := archive.Write(body); err != nil {
				t.Fatal(err)
			}
		}
	})
}

func archiveEntry(t *testing.T, name string, typeflag byte) []byte {
	t.Helper()
	return makeArchive(t, func(archive *tar.Writer) {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Typeflag: typeflag}); err != nil {
			t.Fatal(err)
		}
	})
}

func makeArchive(t *testing.T, write func(*tar.Writer)) []byte {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	archive := tar.NewWriter(gzipWriter)
	write(archive)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
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
