package cli

import (
	"context"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installPlaygroundFixture(t *testing.T, home, script string) {
	t.Helper()
	path := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPlaygroundIsOptionalAndForwardsArguments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{out: &out, errOut: &out})
	_, err := executeWithEnv(env, []string{"playground", "a question"}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "roca plugin install thellmwhisperer/roca-playground") {
		t.Fatalf("missing install hint: %v", err)
	}
	if providerProbe(config.Paths{}, false) != nil {
		t.Fatal("absent plugin registered a provider probe")
	}
	installPlaygroundFixture(t, home, "printf '%s\\n' \"$@\"\n")
	out.Reset()
	_, err = executeWithEnv(env, []string{"playground", "a question", "--full"}, strings.NewReader(""))
	if err != nil || out.String() != "playground\na question\n--full\n" {
		t.Fatalf("delegation: %v %q", err, out.String())
	}
	out.Reset()
	env = hermeticCLIEnv(&cliEnv{out: &out, errOut: &out})
	selected := filepath.Join(home, "selected", "roca.db")
	_, err = executeWithEnv(env, []string{"playground", "question", "--db-path", selected, "--json", "--read-only"}, strings.NewReader(""))
	if err != nil || env.dbPath != selected || !env.json || !env.forceReadOnly {
		t.Fatalf("inherited flags: %v db=%q json=%v readonly=%v", err, env.dbPath, env.json, env.forceReadOnly)
	}
	installPlaygroundFixture(t, home, `printf '%s\n' '{"titular_provider":"fixture","providers":[{"provider":"fixture","ready":true}]}'`+"\n")
	var report service.DoctorReport
	if err := providerProbe(config.Paths{DB: filepath.Join(home, "lab.db")}, false)(context.Background(), &report); err != nil || report.Titular != "fixture" {
		t.Fatalf("probe: %v %+v", err, report)
	}
}

func TestPlaygroundProbeReceivesEffectiveReadOnlyPolicy(t *testing.T) {
	for _, policy := range []string{"flag", "environment"} {
		t.Run(policy, func(t *testing.T) {
			fixtureInstallation(t)
			t.Setenv(config.EnvReadOnly, "")
			if policy == "environment" {
				t.Setenv(config.EnvReadOnly, "1")
			}
			installPlaygroundFixture(t, os.Getenv("HOME"), `
for arg in "$@"; do
  if [ "$arg" = "--read-only" ]; then
    printf '%s\n' '{"titular_provider":"read-only-fixture"}'
    exit 0
  fi
done
exit 1
`)
			env := hermeticCLIEnv(&cliEnv{forceReadOnly: policy == "flag", out: io.Discard, errOut: io.Discard})
			svc, _, err := env.openService()
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			report, err := svc.Doctor(context.Background())
			if err != nil || report.Titular != "read-only-fixture" {
				t.Fatalf("read-only probe: %v %+v", err, report)
			}
		})
	}
}

func TestOpenForPluginPreservesBundledPackageVersions(t *testing.T) {
	fixtureInstallation(t)
	root := filepath.Join(os.Getenv("HOME"), ".roca")
	for _, version := range []string{"0.1.0", "0.2.0"} {
		svc, _, err := OpenForPlugin(Build{Version: version}, filepath.Join(root, "roca.db"), false, io.Discard, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Exec(context.Background(), service.ExecRequest{SQL: "SELECT 1 AS value"}); err != nil {
			t.Fatal(err)
		}
		if err := svc.Close(); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"roca-ops", "roca-corpus"} {
			manifest, err := plugin.ReadManifest(filepath.Join(root, "plugins", name, plugin.PackageFilename))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Version != "test" {
				t.Fatalf("%s version = %q after plugin %s, want core version test", name, manifest.Version, version)
			}
		}
	}
}
