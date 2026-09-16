package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
)

func TestCronListsAndPreviewsTheBundledCoreRide(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, config string
		args, want   []string
		preview      bool
	}{
		{name: "core list and dry-run", config: "[features]\ncron = true\n",
			args: []string{"cron", "list"}, want: []string{"core", "ingest", "nightly", binary}, preview: true},
		{name: "operator ride makes two nightly rides", config: `[features]
cron = true

[ride.vector_delta]
command = "echo operator-vector-delta"
gate = "after_ingest"
`,
			args: []string{"cron", "run", "nightly", "--dry-run"},
			want: []string{"train nightly: 2 rides", "operator", "vector_delta"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ROCA_MODELS_ORDER", "none")
			writeConfig(t, home, test.config)
			if _, err := rocacron.Ensure(filepath.Join(home, ".roca", "plugins"),
				filepath.Join(home, ".local", "bin"), "test"); err != nil {
				t.Fatal(err)
			}
			var output, warnings strings.Builder
			env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: &warnings}
			code, err := executeWithEnv(env, test.args, nil)
			if err != nil || code != ExitOK {
				t.Fatalf("%v = code %d err %v: %s", test.args, code, err, warnings.String())
			}
			for _, want := range test.want {
				if !strings.Contains(output.String(), want) {
					t.Errorf("%v lacks %q: %s", test.args, want, output.String())
				}
			}
			if test.preview {
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
		})
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

func TestCronRunPrintsOperatorRideWarnings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_MODELS_ORDER", "none")
	writeConfig(t, home, "[features]\ncron = true\n")
	ridesDir := filepath.Join(home, ".roca", "rides.d")
	if err := os.MkdirAll(ridesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ridesDir, "broken.toml"), []byte(`[ride.backup]
command = "echo backup"
surprise = true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var output, warnings strings.Builder
	env := &cliEnv{build: Build{Version: "test"}, out: &output, errOut: &warnings}
	code, err := executeWithEnv(env, []string{"cron", "run", "--dry-run"}, nil)
	if err != nil || code != ExitOK {
		t.Fatalf("cron run = code %d err %v: %s%s", code, err, output.String(), warnings.String())
	}
	if !strings.Contains(warnings.String(), "warning:") ||
		!strings.Contains(warnings.String(), "unknown field") {
		t.Fatalf("cron warnings = %q", warnings.String())
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
