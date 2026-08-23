package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

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
}
