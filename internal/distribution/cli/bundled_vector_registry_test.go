package cli

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

func TestInitPlacesBundledVectorBeforeConsentAndLegacyPath(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("fixture uses a POSIX executable")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CI", "")
	t.Setenv("ROCA_VECTOR_STATE_DIR", "")
	legacyRoot := t.TempDir()
	legacy := filepath.Join(legacyRoot, "roca-vector")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\necho legacy companion selected >&2\nexit 9\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(t.TempDir(), "args")
	t.Setenv("ROCA_TEST_VECTOR_ARGS", argsPath)
	payload := []byte("#!/bin/sh\nprintf '%s' \"$*\" > \"$ROCA_TEST_VECTOR_ARGS\"\nprintf '%s\\n' '{\"background\":true}'\n")
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", legacyRoot+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("[features]\nplugins = true\nvector = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	progress, err := os.OpenFile(filepath.Join(t.TempDir(), "progress"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	var output bytes.Buffer
	env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: progress,
		bundledVectorPayload: payload}
	if err := env.offerSemanticSearch(t.Context(), bufio.NewReader(strings.NewReader("yes\n")),
		true, paths, true, &search.Proof{Ready: true, Word: "history", Matches: 1}); err != nil {
		t.Fatal(err)
	}
	arguments, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "install") ||
		strings.Contains(string(arguments), "--stream-progress") {
		t.Fatalf("bundled companion arguments = %q", arguments)
	}
	if !strings.Contains(output.String(), "setup continues in the background") {
		t.Fatalf("semantic setup output = %q", output.String())
	}
}

func TestBundledPluginInstallRefreshesLegacyVectorRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	registryPath := plugin.VectorRegistryPath(pluginRoot(paths))
	if err := os.MkdirAll(filepath.Dir(registryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, []byte(`{"schema":1,"databases":[],"routes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output, dbPath: paths.DB,
		bundledVectorPayload: []byte("#!/bin/sh\nexit 0\n")}
	code, err := executeWithEnv(env, []string{"_install-bundled-plugins", "--json"}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("bundled install = code %d err %v output %q", code, err, output.String())
	}
	registry, err := plugin.LoadVectorRegistry(registryPath)
	if err != nil {
		t.Fatalf("refreshed registry is unusable: %v", err)
	}
	if registry.Schema != 2 || len(registry.Databases) == 0 {
		t.Fatalf("refreshed registry = %+v", registry)
	}
	var corpus *plugin.VectorRegistration
	for index := range registry.Databases {
		if registry.Databases[index].Database == "corpus" {
			corpus = &registry.Databases[index]
			break
		}
	}
	if corpus == nil {
		t.Fatal("refreshed registry has no corpus declaration")
	}
	wantLocal := map[string]string{"memories": "source_session", "exchanges": "session_id"}
	for table, local := range wantLocal {
		var found *plugin.VectorRegistrationTable
		for index := range corpus.Tables {
			if corpus.Tables[index].Name == table {
				found = &corpus.Tables[index]
				break
			}
		}
		if found == nil || found.TimeJoin == nil || found.TimeJoin.Table != "sessions" ||
			found.TimeJoin.LocalColumn != local || found.TimeJoin.ForeignColumn != "session_id" ||
			len(found.TimeJoin.TimeColumns) != 1 || found.TimeJoin.TimeColumns[0] != "started_at" {
			t.Fatalf("%s chronological fallback = %+v", table, found)
		}
	}
}
