package rocavector_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
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
	return vectorFixture{
		root: root, bin: bin,
		executable: filepath.Join(bin, executableName),
		state:      filepath.Join(root, rocavector.Name, rocavector.StateDir, "index.db"),
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
	manifest["source"] = source
	raw, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
