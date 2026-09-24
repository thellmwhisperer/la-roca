package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestMigrateExecutableRefusesNewerOpsSchema(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "roca")
	build := exec.Command("go", "build", "-o", binary, "./cmd/roca")
	build.Dir = "../../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	paths, opsPath := schemaGuardHome(t, "dev")
	command := exec.Command(binary, "--db-path", paths.DB, "migrate")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 ||
		!strings.Contains(string(output), fmt.Sprintf("refusing to migrate roca-ops from schema %d to %d", rocaops.SchemaVersion-1, rocaops.SchemaVersion)) {
		t.Fatalf("migrate refusal: err=%v output=%s", err, output)
	}
	assertOpsSchema(t, opsPath, rocaops.SchemaVersion-1)
	command = exec.Command(binary, "--db-path", paths.DB, "migrate")
	command.Env = append(os.Environ(), bundledplugin.EnvAllowHomeMigrate+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("explicit migrate: %v\n%s", err, output)
	}
	assertOpsSchema(t, opsPath, rocaops.SchemaVersion)
}

func TestBundledInstallAuthorizesSchemaUpgradeWithoutUpdaterOverride(t *testing.T) {
	paths, opsPath := schemaGuardHome(t, "v-installed")
	var output strings.Builder
	env := &cliEnv{out: &output, errOut: &output, dbPath: paths.DB,
		build: Build{Version: "v-new"}, bundledVectorPayload: []byte("#!/bin/sh\nexit 0\n")}
	code, err := executeWithEnv(env, []string{"_install-bundled-plugins", "--json"}, strings.NewReader(""))
	if err != nil || code != ExitOK {
		t.Fatalf("installed release upgrade: code=%d err=%v output=%s", code, err, output.String())
	}
	assertOpsSchema(t, opsPath, rocaops.SchemaVersion)
	if os.Getenv(bundledplugin.EnvAllowHomeMigrate) != "" {
		t.Fatal("installation leaked schema authorization")
	}
}

func schemaGuardHome(t *testing.T, version string) (config.Paths, string) {
	t.Helper()
	home := t.TempDir()
	isolateRuntimeDirs(t, home)
	t.Setenv(bundledplugin.EnvAllowHomeMigrate, "")
	t.Setenv("ROCA_PREFIX", filepath.Join(home, "bin"))
	t.Setenv("ROCA_READ_ONLY", "")
	paths, err := config.Resolve(config.Input{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.DB), 0o700); err != nil {
		t.Fatal(err)
	}
	seedLayoutMemory(t, paths.DB, "schema guard fixture")
	if _, err := rocaops.Ensure(pluginRoot(paths), pluginExecutableDir(paths), version); err != nil {
		t.Fatal(err)
	}
	opsPath := filepath.Join(pluginRoot(paths), rocaops.Name, rocaops.DatabaseFilename)
	db, err := bundledplugin.OpenDatabase(opsPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE plugin_schema SET schema_version = ?`, rocaops.SchemaVersion-1); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return paths, opsPath
}

func assertOpsSchema(t *testing.T, path string, want int) {
	t.Helper()
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow(`SELECT schema_version FROM plugin_schema`).Scan(&version); err != nil || version != want {
		t.Fatalf("ops schema=%d want=%d err=%v", version, want, err)
	}
}
