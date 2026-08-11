package cli

import (
	"os"
	"path/filepath"
	"testing"
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
