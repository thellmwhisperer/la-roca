package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestPluginsResolveFromAControlledPathAndNeverTheCurrentDirectory(t *testing.T) {
	fixtures, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	local := filepath.Join(cwd, "roca-local")
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	t.Setenv("PATH", cwd+string(os.PathListSeparator)+fixtures)

	if _, found := findPlugin("local"); found {
		t.Fatal("resolved a plugin from the current directory")
	}
	path, found := findPlugin("demo")
	if !found || path != filepath.Join(fixtures, "roca-demo") {
		t.Fatalf("demo plugin = %q, found=%v", path, found)
	}
	plugins := listPlugins(config.FeaturesConfig{})
	if len(plugins) != 2 || plugins[0].Name != "demo" || plugins[1].Name != "version" {
		t.Fatalf("plugins = %+v", plugins)
	}
}

func TestVectorExecutableDispatchAndListingRequireItsFeature(t *testing.T) {
	pluginRoot, directory := filepath.Join(t.TempDir(), "plugins"), filepath.Join(t.TempDir(), "bin")
	if _, err := rocavector.EnsureWithPayload(
		pluginRoot, directory, "synthetic-version", []byte("#!/bin/sh\nexit 0\n")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	root := rootCommand(&cliEnv{})

	if handled, _, err := dispatchPlugin(root, []string{"vector"}, config.FeaturesConfig{}); handled || err != nil {
		t.Fatalf("disabled vector dispatch = handled %v, err %v", handled, err)
	}
	if plugins := listPlugins(config.FeaturesConfig{}); len(plugins) != 0 {
		t.Fatalf("disabled vector appeared in plugin listing: %+v", plugins)
	}

	enabled := config.FeaturesConfig{Vector: true}
	if handled, code, err := dispatchPlugin(root, []string{"vector"}, enabled); !handled || code != ExitOK || err != nil {
		t.Fatalf("enabled vector dispatch = handled %v, code %d, err %v", handled, code, err)
	}
	if plugins := listPlugins(enabled); len(plugins) != 1 || plugins[0].Name != "vector" {
		t.Fatalf("enabled vector listing = %+v", plugins)
	}
}

func TestPluginCallsAreAuditedWithoutCredentialArguments(t *testing.T) {
	home, env, warnings := syntheticPluginInstallation(t, 23)
	code, err := executeWithOptions(env,
		[]string{"synthetic-plugin", "--api-token", "not-a-real-credential"}, nil, true)
	if err != nil || code != 23 {
		t.Fatalf("plugin result = code %d err %v, want its exit code", code, err)
	}
	raw := readAuditStream(t, filepath.Join(home, ".roca"), logfile.Executions)
	text := string(raw)
	for _, want := range []string{
		`"command":"synthetic-plugin"`, `"ok":false`, `"exit_code":23`,
		`"args":["--api-token","[REDACTED]"]`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plugin audit lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "not-a-real-credential") {
		t.Fatalf("plugin credential argument leaked: %s", text)
	}
	// A plugin that exited non-zero worded that failure itself, on its own
	// streams. The audit record still names the run, but the seam stays untouched:
	// roca adds no line of its own to what the plugin wrote.
	var record struct {
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.CorrelationID == "" {
		t.Fatalf("a failed plugin left no correlation id in its audit record: %s", text)
	}
	if warnings.Len() != 0 {
		t.Fatalf("roca wrote to a failed plugin's error stream: %q", warnings.String())
	}
}

func TestASuccessfulRunIsNotCorrelated(t *testing.T) {
	_, env, warnings := syntheticPluginInstallation(t, 0)
	code, err := executeWithOptions(env, []string{"synthetic-plugin"}, nil, true)
	if err != nil || code != ExitOK {
		t.Fatalf("plugin result = code %d err %v, want success", code, err)
	}
	if strings.Contains(warnings.String(), "correlation_id") || env.correlation != "" {
		t.Fatalf("a successful run was correlated: %q", warnings.String())
	}
}

// An external plugin that exits with the given code, reachable only through a
// controlled PATH, and a home whose data directory the audit stream lands in.
func syntheticPluginInstallation(t *testing.T, exitCode int) (string, *cliEnv, *strings.Builder) {
	t.Helper()
	home := t.TempDir()
	pluginsDir := t.TempDir()
	plugin := filepath.Join(pluginsDir, "roca-synthetic-plugin")
	if err := os.WriteFile(plugin, fmt.Appendf(nil, "#!/bin/sh\nexit %d\n", exitCode), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pluginsDir)
	t.Setenv("HOME", home)
	warnings := &strings.Builder{}
	return home, &cliEnv{out: &strings.Builder{}, errOut: warnings}, warnings
}
