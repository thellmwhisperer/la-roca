package rocavector_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestPayloadEnvelopeRoundTripsAndReplacesTheExecutable(t *testing.T) {
	core := filepath.Join(t.TempDir(), "roca")
	payload := filepath.Join(t.TempDir(), "roca-vector")
	if err := os.WriteFile(core, []byte("synthetic core"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"vector one", "vector two"} {
		if err := os.WriteFile(payload, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := rocavector.AppendPayload(core, payload); err != nil {
			t.Fatal(err)
		}
		got, err := rocavector.ReadPayload(core)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("payload = %q, want %q", got, body)
		}
	}
}

func TestBundledVectorRefreshAndCollisionPolicy(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		external  bool
		wantError string
		wantBody  string
	}{
		{name: "lockstep refresh preserves state", wantBody: "vector two"},
		{name: "external installation is surfaced", external: true,
			wantError: "collides with an installation from", wantBody: "vector one"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := installVectorFixture(t)
			if err := os.WriteFile(fixture.state, []byte("preserved index"), 0o600); err != nil {
				t.Fatal(err)
			}
			if testCase.external {
				rewriteSource(t, filepath.Join(fixture.root, rocavector.Name), "/synthetic/vector-package")
			}

			fixture.ensureAndAssert(t, "v2", []byte("vector two"), testCase.wantError, testCase.wantBody)
			assertFileContents(t, "state", fixture.state, "preserved index")
			manifest, readErr := plugininstall.ReadManifest(filepath.Join(fixture.root, rocavector.Name))
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantVersion := "v2"
			if testCase.external {
				wantVersion = "v1"
			}
			if manifest.Version != wantVersion {
				t.Fatalf("manifest version = %q, want %q", manifest.Version, wantVersion)
			}
		})
	}
}

func TestWorkerRunningReadsCurrentClaimFormat(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, rocavector.Name, rocavector.StateDir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, ".worker"),
		[]byte(strconv.Itoa(os.Getpid())+" current-run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !rocavector.WorkerRunning(root) {
		t.Fatal("current worker claim was reported stopped")
	}
}

func TestBundledVectorMigratesLegacyInstall(t *testing.T) {
	for _, testCase := range []struct {
		name, version, payload, wantError string
		plantLegacy, plantCurrent         bool
		external, unmanaged               bool
		corruptPayload, changeExecutable  bool
		activeWorker, interruptedRename   bool
		interruptedUpdate                 bool
		wantLegacy, wantCurrent           bool
		preserveState                     bool
		twice                             bool
	}{
		{name: "upgrade with rename preserves state", version: "v2", payload: "vector two",
			plantLegacy: true, preserveState: true, wantCurrent: true},
		{name: "second run is a no-op", version: "v2", payload: "vector two",
			plantLegacy: true, preserveState: true, twice: true, wantCurrent: true},
		{name: "both directories refuse", version: "v2", payload: "vector two",
			plantLegacy: true, plantCurrent: true, wantError: "both",
			wantLegacy: true, wantCurrent: true},
		{name: "external legacy is not migrated", version: "v2", payload: "vector two",
			plantLegacy: true, external: true, wantError: "collides with an installation from",
			wantLegacy: true},
		{name: "unmanaged leftover is not migrated", version: "v2", payload: "vector two",
			plantLegacy: true, unmanaged: true, wantError: "cannot replace an unmanaged directory",
			wantLegacy: true},
		{name: "corrupt legacy payload is not migrated", version: "v2", payload: "vector two",
			plantLegacy: true, corruptPayload: true, wantError: "checksum mismatch",
			wantLegacy: true},
		{name: "changed legacy executable is not migrated", version: "v2", payload: "vector two",
			plantLegacy: true, changeExecutable: true, wantError: "changed since install",
			wantLegacy: true},
		{name: "active legacy worker blocks migration", version: "v2", payload: "vector two",
			plantLegacy: true, activeWorker: true, wantError: "active vector worker",
			wantLegacy: true},
		{name: "rename interruption converges", version: "v2", payload: "vector two",
			plantLegacy: true, preserveState: true, interruptedRename: true, wantCurrent: true},
		{name: "update interruption converges", version: "v2", payload: "vector two",
			plantLegacy: true, preserveState: true, interruptedUpdate: true, wantCurrent: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
			if testCase.plantLegacy && !testCase.unmanaged {
				plantLegacyVector(t, root, bin, "v1", []byte("vector one"))
			} else if testCase.unmanaged {
				if err := os.MkdirAll(filepath.Join(root, rocavector.LegacyName, "noise"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.plantCurrent {
				if err := os.MkdirAll(filepath.Join(root, rocavector.Name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			legacyState := filepath.Join(root, rocavector.LegacyName, rocavector.StateDir, "index.db")
			currentState := filepath.Join(root, rocavector.Name, rocavector.StateDir, "index.db")
			var before os.FileInfo
			if testCase.preserveState {
				if err := os.WriteFile(legacyState, []byte("preserved index"), 0o600); err != nil {
					t.Fatal(err)
				}
				var err error
				before, err = os.Lstat(legacyState)
				if err != nil {
					t.Fatal(err)
				}
			}
			if testCase.external {
				rewriteSource(t, filepath.Join(root, rocavector.LegacyName), "/synthetic/vector-package")
			}
			if testCase.corruptPayload {
				if err := os.WriteFile(filepath.Join(root, rocavector.LegacyName, plugininstall.PackageFilename),
					[]byte("corrupted"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.changeExecutable {
				executable := plugininstall.ExecutableName(rocavector.LegacyName)
				if runtime.GOOS == "windows" {
					executable += ".exe"
				}
				if err := os.WriteFile(filepath.Join(bin, executable), []byte("locally changed"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.activeWorker {
				if err := os.WriteFile(filepath.Join(root, rocavector.LegacyName, rocavector.StateDir, ".worker"),
					[]byte(strconv.Itoa(os.Getpid())+" current-run\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if testCase.interruptedRename || testCase.interruptedUpdate {
				legacy := filepath.Join(root, rocavector.LegacyName)
				current := filepath.Join(root, rocavector.Name)
				if err := os.Rename(legacy, current); err != nil {
					t.Fatal(err)
				}
				if testCase.interruptedUpdate {
					rewriteName(t, current, rocavector.Name)
					if err := os.Rename(current, filepath.Join(root, "."+rocavector.Name+".previous")); err != nil {
						t.Fatal(err)
					}
				}
			}

			_, err := rocavector.EnsureWithPayload(root, bin, testCase.version, []byte(testCase.payload))
			if testCase.twice && err == nil {
				_, err = rocavector.EnsureWithPayload(root, bin, testCase.version, []byte(testCase.payload))
			}
			if testCase.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantError != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantError)) {
				t.Fatalf("ensure error = %v, want %q", err, testCase.wantError)
			}
			assertDirExists(t, filepath.Join(root, rocavector.LegacyName), testCase.wantLegacy)
			assertDirExists(t, filepath.Join(root, rocavector.Name), testCase.wantCurrent)
			if testCase.preserveState && testCase.wantError == "" {
				after, statErr := os.Lstat(currentState)
				if statErr != nil {
					t.Fatal(statErr)
				}
				assertStateIdentity(t, before, after)
				assertFileContents(t, "state", currentState, "preserved index")
			}
			if testCase.wantCurrent && testCase.wantError == "" {
				entries, readErr := os.ReadDir(bin)
				if readErr != nil {
					t.Fatal(readErr)
				}
				family := "roca-"
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), family+family) {
						t.Fatalf("double-prefixed executable appeared: %s", entry.Name())
					}
				}
				manifest, readErr := plugininstall.ReadManifest(filepath.Join(root, rocavector.Name))
				if readErr != nil || manifest.Name != rocavector.Name {
					t.Fatalf("migrated manifest = %+v, err=%v", manifest, readErr)
				}
			}
		})
	}
}

func TestBundledVectorRestoresLegacyNameWhenApplyFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory modes")
	}
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	plantLegacyVector(t, root, bin, "v1", []byte("vector one"))
	state := filepath.Join(root, rocavector.LegacyName, rocavector.StateDir, "index.db")
	if err := os.WriteFile(state, []byte("preserved index"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(bin, 0o700)

	if _, err := rocavector.EnsureWithPayload(root, bin, "v2", []byte("vector two")); err == nil {
		t.Fatal("apply failure unexpectedly succeeded")
	}
	assertDirExists(t, filepath.Join(root, rocavector.LegacyName), true)
	assertDirExists(t, filepath.Join(root, rocavector.Name), false)
	after, err := os.Lstat(state)
	if err != nil {
		t.Fatal(err)
	}
	assertStateIdentity(t, before, after)
	manifest, err := plugininstall.ReadManifest(filepath.Join(root, rocavector.LegacyName))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Name != rocavector.LegacyName {
		t.Fatalf("restored manifest name = %q, want %q", manifest.Name, rocavector.LegacyName)
	}
}

func TestBundledVectorRepairsOnlyAMissingSameVersionExecutable(t *testing.T) {
	for _, testCase := range []struct {
		name, replacement, wantError, wantBody string
		remove                                 bool
	}{
		{name: "missing executable is repaired", remove: true, wantBody: "vector one"},
		{name: "changed executable is refused", replacement: "locally changed",
			wantError: "changed since install", wantBody: "locally changed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := installVectorFixture(t)
			if testCase.remove {
				if err := os.Remove(fixture.executable); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(fixture.executable, []byte(testCase.replacement), 0o700); err != nil {
				t.Fatal(err)
			}

			fixture.ensureAndAssert(t, "v1", []byte("vector one"), testCase.wantError, testCase.wantBody)
		})
	}
}

type vectorFixture struct {
	root, bin, executable, state string
}

func installVectorFixture(t *testing.T) vectorFixture {
	t.Helper()
	root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	if _, err := rocavector.EnsureWithPayload(root, bin, "v1", []byte("vector one")); err != nil {
		t.Fatal(err)
	}
	executableName := "roca-vector"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	fixture := vectorFixture{
		root: root, bin: bin,
		executable: filepath.Join(bin, executableName),
		state:      filepath.Join(root, rocavector.Name, rocavector.StateDir, "index.db"),
	}
	if _, err := os.Stat(filepath.Join(root, rocavector.LegacyName)); !os.IsNotExist(err) {
		t.Fatalf("fresh install left a leftover %s directory: %v", rocavector.LegacyName, err)
	}
	return fixture
}

func plantLegacyVector(t *testing.T, root, bin, version string, payload []byte) {
	t.Helper()
	spec := bundledplugin.Spec{
		Name: rocavector.LegacyName, Executable: plugininstall.ExecutableName(rocavector.LegacyName),
		Source:   plugin.BundledSource,
		Manifest: []byte(`{"schema":1,"name":"vector","version":"dev","kind":"executable","state_directory":"state"}`),
		Payload:  func() ([]byte, error) { return payload, nil },
	}
	if runtime.GOOS == "windows" {
		spec.Executable += ".exe"
	}
	if _, err := bundledplugin.Ensure(root, bin, version, spec); err != nil {
		t.Fatal(err)
	}
}

func assertDirExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if want && err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
	if !want && !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent: %v", path, err)
	}
}

func assertStateIdentity(t *testing.T, before, after os.FileInfo) {
	t.Helper()
	sameFile := os.SameFile(before, after)
	sizes := [2]int64{before.Size(), after.Size()}
	if !sameFile || sizes[0] != sizes[1] {
		t.Fatalf("state identity changed: same=%v size %d -> %d", sameFile, sizes[0], sizes[1])
	}
}

func (fixture vectorFixture) ensureAndAssert(
	t *testing.T,
	version string,
	payload []byte,
	wantError, wantBody string,
) {
	t.Helper()
	_, err := rocavector.EnsureWithPayload(fixture.root, fixture.bin, version, payload)
	if wantError == "" && err != nil {
		t.Fatal(err)
	}
	if wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
		t.Fatalf("ensure error = %v, want %q", err, wantError)
	}
	assertFileContents(t, "executable", fixture.executable, wantBody)
}

func assertFileContents(t *testing.T, label, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("%s = %q, err=%v", label, raw, err)
	}
}

func rewriteSource(t *testing.T, directory, source string) {
	rewriteManifest(t, directory, "source", source)
}

func rewriteName(t *testing.T, directory, name string) {
	rewriteManifest(t, directory, "name", name)
}

func rewriteManifest(t *testing.T, directory, key, value string) {
	t.Helper()
	path := filepath.Join(directory, plugininstall.ManifestFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest[key] = value
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
