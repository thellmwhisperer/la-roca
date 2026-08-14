package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
)

func TestCronListsAndPreviewsTheBundledCoreRide(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_MODELS_ORDER", "none")
	writeConfig(t, home, "[features]\ncron = true\n")
	if _, err := rocacron.Ensure(filepath.Join(home, ".roca", "plugins"),
		filepath.Join(home, ".local", "bin"), "test"); err != nil {
		t.Fatal(err)
	}

	var output, warnings strings.Builder
	env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: &warnings}
	code, err := executeWithEnv(env, []string{"cron", "list"}, nil)
	if err != nil || code != ExitOK {
		t.Fatalf("cron list = code %d err %v: %s", code, err, warnings.String())
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"core", "ingest", "nightly", binary} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("cron list lacks %q: %s", want, output.String())
		}
	}

	output.Reset()
	code, err = executeWithEnv(env, []string{"cron", "run", "--dry-run"}, nil)
	if err != nil || code != ExitOK || !strings.Contains(output.String(), "ready") {
		t.Fatalf("cron dry run = code %d err %v: %s%s", code, err, output.String(), warnings.String())
	}
	db := filepath.Join(home, ".roca", "plugins", rocacron.Name, rocacron.DatabaseFilename)
	if info, err := os.Stat(db); err != nil || info.Size() == 0 {
		t.Fatalf("journey database = %v, %v", info, err)
	}
}

func TestCronListAndDryRunRemainAvailableInReadOnlyMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_MODELS_ORDER", "none")
	t.Setenv("ROCA_READ_ONLY", "1")
	writeConfig(t, home, "[features]\ncron = true\n")

	var output, warnings strings.Builder
	env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: &warnings}
	for _, args := range [][]string{{"cron", "list"}, {"cron", "run", "--dry-run"}} {
		output.Reset()
		code, err := executeWithEnv(env, args, nil)
		if err != nil || code != ExitOK || !strings.Contains(output.String(), "ingest") {
			t.Fatalf("%v in read-only mode = code %d err %v: %s%s",
				args, code, err, output.String(), warnings.String())
		}
	}
	pluginDirectory := filepath.Join(home, ".roca", "plugins", rocacron.Name)
	if _, err := os.Stat(pluginDirectory); !os.IsNotExist(err) {
		t.Fatalf("read-only inspection installed the plugin: %v", err)
	}
}

func TestCronCommandDoesNotExistUntilItsFeatureIsEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, body := range []string{"", "[features]\ncron = false\n"} {
		writeConfig(t, home, body)
		var output strings.Builder
		code, err := executeWithEnv(&cliEnv{out: &output, errOut: &output}, []string{"cron", "list"}, nil)
		if err == nil || code == ExitOK || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("cron with config %q = code %d err %v: %s", body, code, err, output.String())
		}
	}
}
