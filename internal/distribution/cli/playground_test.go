package cli

import (
	"context"
	"encoding/json"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/playground"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installPlaygroundFixture(t *testing.T, home, script string) {
	t.Helper()
	path := filepath.Join(home, ".roca", "plugins", "roca-playground", "roca-playground")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+`audit='{"stderr":""}'
if [ "$1" = "--transport" ]; then
 shift
 trap 'printf "%s\n" "$audit" >&2' EXIT
fi
`+script), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPlaygroundIsOptionalAndForwardsArguments(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
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
	selected := filepath.Join(home, ".roca", "roca.db")
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

func TestPlaygroundPreparesLegacyCorpusAndKeepsEndOfOptions(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
	root := filepath.Join(home, ".roca")
	if err := os.RemoveAll(filepath.Join(root, "plugins", "roca-corpus")); err != nil {
		t.Fatal(err)
	}
	installPlaygroundFixture(t, home, `test -f "$HOME/.roca/plugins/roca-corpus/plugin.json" || exit 9
printf '%s\n' "$@"
`)
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: Build{Version: "test"}, out: &out, errOut: &out})
	db := filepath.Join(root, "roca.db")
	code, err := executeWithEnv(env, []string{"--db-path", db, "--json", "playground", "--", "who wrote this?"}, strings.NewReader(""))
	want := "playground\n--db-path\n" + db + "\n--json\n--db-path\n" + db + "\n--json\n--\nwho wrote this?\n"
	if code != 0 || err != nil || out.String() != want {
		t.Fatalf("code=%d err=%v output=%q want=%q", code, err, out.String(), want)
	}
}

func TestPlaygroundPreservesAuditWithoutDuplicateRecords(t *testing.T) {
	fixtureInstallation(t)
	home := os.Getenv("HOME")
	installPlaygroundFixture(t, home, `audit='{"stderr":"","cleaned_sql":"SELECT 7","query":{"question":"fixture","path":"model","model_sql":"SELECT original","sql_provider":"fixture-provider","sql_model":"fixture-model","retried_sql":true,"retry_type":"gate_rejection","degraded":"model_query_error","provider_error":"fixture failure"}}'
printf 'fixture output\n'
exit 1
`)
	var out strings.Builder
	env := hermeticCLIEnv(&cliEnv{build: Build{Version: "test"}, out: &out, errOut: &out})
	code, err := executeWithEnv(env, []string{"playground", "fixture"}, strings.NewReader(""))
	if code != 1 || err != nil || !strings.HasPrefix(out.String(), "fixture output\ncorrelation_id: ") {
		t.Fatalf("code=%d err=%v output=%q", code, err, out.String())
	}
	files, err := filepath.Glob(filepath.Join(home, ".roca", logfile.DirName, "executions-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("logs=%v err=%v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var record logfile.ExecutionRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Command != "playground" {
			continue
		}
		count++
		if record.SQL != "SELECT 7" || record.SQLProvider != "fixture-provider" || !record.RetriedSQL || record.ErrorType != "model_query_error" {
			t.Fatalf("audit=%s", line)
		}
	}
	if count != 1 {
		t.Fatalf("playground audit records=%d", count)
	}
}

func TestPlaygroundModelPromptArrivesBeforeInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installPlaygroundFixture(t, home, `printf 'choose model\n' >&2
read reply
printf 'selected %s\n' "$reply"
`)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	input, answer := io.Pipe()
	defer input.Close()
	diagnostic, prompt := io.Pipe()
	defer diagnostic.Close()
	go func() { <-ctx.Done(); answer.Close() }()
	seen := make(chan bool, 1)
	go func() {
		message := make([]byte, len("choose model\n"))
		_, err := io.ReadFull(diagnostic, message)
		seen <- err == nil && string(message) == "choose model\n"
		if err == nil {
			_, _ = io.WriteString(answer, "fixture\n")
		}
		answer.Close()
	}()
	var out strings.Builder
	audit, err := playground.Run(ctx, []string{"model"}, input, &out, prompt)
	prompt.Close()
	if err != nil || audit != nil || !<-seen || out.String() != "selected fixture\n" {
		t.Fatalf("model prompt was buffered: audit=%v err=%v output=%q", audit, err, out.String())
	}
}
