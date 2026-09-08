package cli

import (
	"context"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
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
	if providerProbe(config.Paths{}) != nil {
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
	if err := providerProbe(config.Paths{DB: filepath.Join(home, "lab.db")})(context.Background(), &report); err != nil || report.Titular != "fixture" {
		t.Fatalf("probe: %v %+v", err, report)
	}
}
