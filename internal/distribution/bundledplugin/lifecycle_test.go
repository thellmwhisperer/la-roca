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

func executableSpec(name string, payload []byte) bundledplugin.Spec {
	return bundledplugin.Spec{
		Name: name, Executable: executableName(name), Source: "bundled:roca",
		Manifest: []byte(`{"schema":1,"kind":"executable","state_directory":"state"}`),
		Payload:  func() ([]byte, error) { return payload, nil },
	}
}

func executableName(name string) string {
	executable := "roca-" + name
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	return executable
}
