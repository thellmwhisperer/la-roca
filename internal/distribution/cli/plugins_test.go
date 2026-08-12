package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
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
	plugins := listPlugins()
	if len(plugins) != 2 || plugins[0].Name != "demo" || plugins[1].Name != "version" {
		t.Fatalf("plugins = %+v", plugins)
	}
}

func TestPluginCallsAreAuditedWithoutCredentialArguments(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".roca")
	pluginsDir := t.TempDir()
	plugin := filepath.Join(pluginsDir, "roca-synthetic-plugin")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 23\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pluginsDir)
	t.Setenv("HOME", home)
	var warnings strings.Builder
	env := &cliEnv{
		out: &strings.Builder{}, errOut: &warnings,
	}
	code, err := executeWithOptions(env,
		[]string{"synthetic-plugin", "--api-token", "not-a-real-credential"}, nil, true)
	if err != nil || code != 23 {
		t.Fatalf("plugin result = code %d err %v, want its exit code", code, err)
	}
	matches, err := filepath.Glob(filepath.Join(dataDir, logfile.DirName, "executions-*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("execution logs = %v, err=%v, warnings=%q", matches, err, warnings.String())
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
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
	// A plugin that exited non-zero surfaced a failure roca never worded, so the
	// boundary that logs it is the one that has to name the log line.
	var record struct {
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.CorrelationID == "" {
		t.Fatalf("a failed plugin left no correlation id in its audit record: %s", text)
	}
	if !strings.Contains(warnings.String(), record.CorrelationID) {
		t.Fatalf("the run does not name its audit line %q on the error stream: %q",
			record.CorrelationID, warnings.String())
	}
}

func TestASuccessfulRunIsNotCorrelated(t *testing.T) {
	home := t.TempDir()
	pluginsDir := t.TempDir()
	plugin := filepath.Join(pluginsDir, "roca-synthetic-plugin")
	if err := os.WriteFile(plugin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pluginsDir)
	t.Setenv("HOME", home)
	var warnings strings.Builder
	env := &cliEnv{out: &strings.Builder{}, errOut: &warnings}
	code, err := executeWithOptions(env, []string{"synthetic-plugin"}, nil, true)
	if err != nil || code != ExitOK {
		t.Fatalf("plugin result = code %d err %v, want success", code, err)
	}
	if strings.Contains(warnings.String(), "correlation_id") || env.correlation != "" {
		t.Fatalf("a successful run was correlated: %q", warnings.String())
	}
}
