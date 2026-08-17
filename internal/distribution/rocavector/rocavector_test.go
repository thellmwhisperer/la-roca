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
			root, bin := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
			if _, err := rocavector.EnsureWithPayload(root, bin, "v1", []byte("vector one")); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, rocavector.Name, rocavector.StateDir, "index.db")
			if err := os.WriteFile(state, []byte("preserved index"), 0o600); err != nil {
				t.Fatal(err)
			}
			if testCase.external {
				rewriteSource(t, filepath.Join(root, rocavector.Name), "/synthetic/vector-package")
			}

			_, err := rocavector.EnsureWithPayload(root, bin, "v2", []byte("vector two"))
			if testCase.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantError != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantError)) {
				t.Fatalf("refresh error = %v, want %q", err, testCase.wantError)
			}
			executableName := "roca-vector"
			if runtime.GOOS == "windows" {
				executableName += ".exe"
			}
			executable := filepath.Join(bin, executableName)
			if raw, readErr := os.ReadFile(executable); readErr != nil || string(raw) != testCase.wantBody {
				t.Fatalf("executable = %q, err=%v", raw, readErr)
			}
			if raw, readErr := os.ReadFile(state); readErr != nil || string(raw) != "preserved index" {
				t.Fatalf("state = %q, err=%v", raw, readErr)
			}
			manifest, readErr := plugininstall.ReadManifest(filepath.Join(root, rocavector.Name))
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
