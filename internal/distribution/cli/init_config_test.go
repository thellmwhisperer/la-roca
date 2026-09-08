package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestTTYInitPreservesExistingConfigByteExact(t *testing.T) {
	home, _ := initChooserHome(t)
	path := filepath.Join(home, ".roca", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "# operator note\n[models]\nprobe_ms = 500\norder = [\"ollama\"]\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(home, ".roca", "roca.db")

	out, err := runInitChooser(t, true, "sonnet\n\n", chooserTestBackend{},
		"init", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if strings.Contains(out, "Which model") || strings.Contains(out, "configuration updated") {
		t.Fatalf("init offered to replace an existing configuration:\n%s", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != before {
		t.Fatalf("init changed the existing config:\n--- want ---\n%s--- got ---\n%s", before, raw)
	}
	if _, err := os.Stat(path + ".roca.bak"); !os.IsNotExist(err) {
		t.Fatalf("init created a backup for an unchanged config: %v", err)
	}
}

func TestInitIgnoresConfigOverrideWhenCreatingExplicitDatabaseConfig(t *testing.T) {
	home, _ := initChooserHome(t)
	override := filepath.Join(home, "override", "config.toml")
	if err := os.MkdirAll(filepath.Dir(override), 0o700); err != nil {
		t.Fatal(err)
	}
	before := "# shared operator config\n[features]\nvector = true\n"
	if err := os.WriteFile(override, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROCA_CONFIG", override)
	out, configPath := runExplicitNonTTYInit(t, home)
	if !strings.Contains(out, "configuration: "+configPath) {
		t.Fatalf("init did not report the database-adjacent config:\n%s", out)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "[features]\nplugins = true\nroca_ops = true\ncron = true\nvector = false\n"
	if string(raw) != want {
		t.Fatalf("database-adjacent config:\n--- want ---\n%s--- got ---\n%s", want, raw)
	}
	shared, err := os.ReadFile(override)
	if err != nil {
		t.Fatal(err)
	}
	if string(shared) != before {
		t.Fatalf("init changed the ROCA_CONFIG file:\n--- want ---\n%s--- got ---\n%s", before, shared)
	}
}

func TestInitPreservesConfigCreatedDuringDatabasePrompt(t *testing.T) {
	home, _ := initChooserHome(t)
	configPath := filepath.Join(home, ".roca", "config.toml")
	operatorConfig := "[features]\nplugins = false\nroca_ops = false\ncron = false\nvector = true\n"
	input := &firstReadHook{
		reader: strings.NewReader("new\n"),
		hook: func() {
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte(operatorConfig), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}

	out, err := runInitChooserReader(t, true, input, chooserTestBackend{}, "init")
	if err == nil || !strings.Contains(err.Error(), "appeared before it could be created") ||
		!strings.Contains(err.Error(), "existing file was preserved") {
		t.Fatalf("init error = %v, want ownership collision:\n%s", err, out)
	}
	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != operatorConfig {
		t.Fatalf("init changed the concurrently created config:\n--- want ---\n%s--- got ---\n%s", operatorConfig, after)
	}
}

func TestInitCreatesConfigBesideEnvironmentDatabase(t *testing.T) {
	home, bin := initChooserHome(t)
	fakeModelCLI(t, bin, "claude")
	dbPath := filepath.Join(home, "environment", "roca.db")
	configPath := filepath.Join(filepath.Dir(dbPath), "config.toml")
	t.Setenv("ROCA_DB_PATH", dbPath)

	out, err := runInitChooser(t, true, "new\n\n\n", chooserTestBackend{}, "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	if !strings.Contains(out, "configuration: "+configPath) {
		t.Fatalf("init did not use the environment database config:\n%s", out)
	}
	assertInitFeatures(t, configPath, "environment database")
	if _, err := os.Stat(filepath.Join(home, ".roca", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote the home config instead: %v", err)
	}
}

func TestInitPreservesExistingDatabaseDirectoryMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not portable to Windows")
	}
	home, _ := initChooserHome(t)
	dataDir := filepath.Join(home, "shared-data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "roca.db")
	out, err := runInitChooser(t, false, "", chooserTestBackend{},
		"init", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("database directory mode = %o, want 750", got)
	}
	configInfo, err := os.Stat(filepath.Join(dataDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := configInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func initChooserHome(t *testing.T) (string, string) {
	t.Helper()
	home, bin := t.TempDir(), t.TempDir()
	isolateRuntimeDirs(t, home)
	t.Setenv("PATH", bin)
	t.Setenv("ROCA_DB_PATH", "")
	t.Setenv("ROCA_CONFIG", "")
	t.Setenv("ROCA_MODELS_ORDER", "")
	t.Setenv("ROCA_CODEX_MODEL", "")
	t.Setenv("ROCA_OLLAMA_MODEL", "")
	t.Setenv("ROCA_MODEL", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[{"name":"local-one"},{"name":"environment-model"},{"name":"local-fallback"}]}`))
		case "/api/chat":
			_, _ = w.Write([]byte(`{"message":{"content":"SELECT 1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("ROCA_OLLAMA_BASE_URL", server.URL)
	return home, bin
}

func fakeModelCLI(t *testing.T, bin, name string) {
	t.Helper()
	body := "#!/bin/sh\nprintf 'SELECT 1\\n'\n"
	if name == "claude" {
		body = "#!/bin/sh\nprintf '{\"result\":\"SELECT 1\"}\\n'\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func runInitChooser(t *testing.T, tty bool, input string, backend any,
	args ...string) (string, error) {
	return runInitChooserReader(t, tty, strings.NewReader(input), backend, args...)
}

func runInitChooserReader(t *testing.T, tty bool, input io.Reader, backend any,
	args ...string) (string, error) {
	t.Helper()
	previous := terminalInput
	terminalInput = func(any) bool { return tty }
	t.Cleanup(func() { terminalInput = previous })
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{
		build: Build{Version: "test", Commit: "test-sha"}, out: &out, errOut: &out,
	})
	env.skipInitChooser = false
	env.skipReconciliation = false
	_, err := executeWithEnv(env, args, input)
	return out.String(), err
}

type firstReadHook struct {
	reader io.Reader
	hook   func()
}

func (reader *firstReadHook) Read(buffer []byte) (int, error) {
	if reader.hook != nil {
		hook := reader.hook
		reader.hook = nil
		hook()
	}
	return reader.reader.Read(buffer)
}

func countLinesWithPrefix(text, prefix string) int {
	count := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

type chooserTestBackend struct{}

func runExplicitNonTTYInit(t *testing.T, home string) (string, string) {
	t.Helper()
	dbPath := filepath.Join(home, "explicit", "roca.db")
	out, err := runInitChooser(t, false, "", chooserTestBackend{},
		"init", "--db-path", dbPath)
	if err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return out, filepath.Join(filepath.Dir(dbPath), "config.toml")
}

func assertInitFeatures(t *testing.T, configPath, label string) {
	t.Helper()
	file, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !file.Features.Plugins || !file.Features.RocaOps || !file.Features.Cron || file.Features.Vector {
		t.Fatalf("%s config has wrong features: %+v", label, file.Features)
	}
}
