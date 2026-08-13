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

func TestPluginConsentDistinguishesDataFromCodeAndNamesTheReplacedChecksum(t *testing.T) {
	checksum, recorded := strings.Repeat("a", 64), strings.Repeat("b", 64)
	tests := []struct {
		action  string
		risk    plugininstall.Risk
		trusted string
		want    []string
	}{
		{"install", plugininstall.DataOnly, "",
			[]string{"DATA-ONLY: near-harmless; its worst case is lying content", "sha256:" + checksum}},
		{"install", plugininstall.Executable, "",
			[]string{"EXECUTABLE: FULL TRUST; it runs code with your user privileges"}},
		{"update", plugininstall.DataOnly, recorded,
			[]string{"sha256:" + checksum, "replaces the recorded sha256:" + recorded}},
		{"update", plugininstall.DataOnly, checksum,
			[]string{"unchanged since the recorded install"}},
	}
	for _, test := range tests {
		candidate := plugininstall.Candidate{
			Name: "synthetic", Version: "1.2.3", Source: "owner/synthetic",
			Checksum: checksum, Risk: test.risk,
		}
		text := pluginConsentText(test.action, candidate, test.trusted)
		wanted := append([]string{"source: owner/synthetic", "version: 1.2.3"}, test.want...)
		for _, want := range wanted {
			if !strings.Contains(text, want) {
				t.Errorf("%s %s consent lacks %q:\n%s", test.action, test.risk, want, text)
			}
		}
	}
}
