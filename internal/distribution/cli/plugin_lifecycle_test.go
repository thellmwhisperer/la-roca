package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
)

func TestPluginInstallerIsInertBeforeTheExperimentalFlag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var output, warnings strings.Builder
	env := &cliEnv{out: &output, errOut: &warnings}
	code, err := executeWithEnv(env, []string{"plugin", "install", filepath.Join(home, "absent")}, strings.NewReader("yes\n"))
	if err == nil || code != ExitError || !strings.Contains(err.Error(), "features.plugins") {
		t.Fatalf("disabled install = code %d err %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".roca", "plugins")); !os.IsNotExist(err) {
		t.Fatalf("disabled installer touched the plugin directory: %v", err)
	}
}

func TestPluginConsentDistinguishesDataFromCode(t *testing.T) {
	tests := []struct {
		risk plugininstall.Risk
		want string
	}{
		{plugininstall.DataOnly, "DATA-ONLY: near-harmless; its worst case is lying content"},
		{plugininstall.Executable, "EXECUTABLE: FULL TRUST; it runs code with your user privileges"},
	}
	for _, test := range tests {
		candidate := plugininstall.Candidate{
			Name: "synthetic", Version: "1.2.3", Source: "owner/synthetic",
			Checksum: strings.Repeat("a", 64), Risk: test.risk,
		}
		text := pluginConsentText("install", candidate)
		for _, want := range []string{"source: owner/synthetic", "version: 1.2.3", "sha256:" + candidate.Checksum, test.want} {
			if !strings.Contains(text, want) {
				t.Errorf("%s consent lacks %q:\n%s", test.risk, want, text)
			}
		}
	}
}
