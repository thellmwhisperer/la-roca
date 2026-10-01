package cli

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	_ "modernc.org/sqlite"
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
			ensureCronInstalled(t, home)
			env, output, warnings := newCronTestEnv()
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

	env, output, warnings := newCronTestEnv()
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

func TestCronHourlyVectorDeltaRecordsAFailedShellRide(t *testing.T) {
	setupUnixCronRide(t, `command = "echo vector-delta-progress >&2; exit 1"`)
	env, output, warnings := newCronTestEnv()
	code, err := executeWithEnv(env, []string{"cron", "run", "hourly"}, nil)
	if err != nil || code != ExitError ||
		!strings.Contains(output.String(), "vector_delta") ||
		!strings.Contains(output.String(), "exit=1") ||
		!strings.Contains(warnings.String(), "vector-delta-progress") {
		t.Fatalf("hourly vector_delta = code %d err %v out=%q errOut=%q",
			code, err, output.String(), warnings.String())
	}
}

func TestCronRunRejectsAnUnusableOperatorRideFile(t *testing.T) {
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

	env, output, warnings := newCronTestEnv()
	code, err := executeWithEnv(env, []string{"cron", "run", "--dry-run"}, nil)
	if err == nil || code == ExitOK || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("cron run = code %d err %v: %s%s", code, err, output.String(), warnings.String())
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

func TestRideBinaryInvocationMatchesVectorSubcommandPosition(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"vector", "ingest"}, want: true},
		{args: []string{"vector", "query", "ingest"}, want: false},
		{args: []string{"ingest"}, want: true},
	} {
		if got := rideBinaryInvocation(test.args); got != test.want {
			t.Errorf("rideBinaryInvocation(%v) = %t, want %t", test.args, got, test.want)
		}
	}
}

func newCronTestEnv() (*cliEnv, *strings.Builder, *strings.Builder) {
	output := &strings.Builder{}
	warnings := &strings.Builder{}
	return &cliEnv{build: Build{Version: "test"}, out: output, errOut: warnings}, output, warnings
}

func ensureCronInstalled(t *testing.T, home string) {
	t.Helper()
	if _, err := rocacron.Ensure(filepath.Join(home, ".roca", "plugins"),
		filepath.Join(home, ".local", "bin"), "test"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectSchedulerInvocationNamesTheCronRunRemedy(t *testing.T) {
	for _, parent := range []string{"/usr/sbin/cron", "crond"} {
		t.Run(parent, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("ROCA_MODELS_ORDER", "none")
			t.Setenv(rocacron.ObservedEnv, "")
			writeConfig(t, home, "[features]\ncron = true\n")
			previous := schedulerAncestry
			schedulerAncestry = func() []authorshipProcess {
				return []authorshipProcess{{Command: parent}}
			}
			t.Cleanup(func() { schedulerAncestry = previous })
			for _, args := range [][]string{
				{"ingest"},
				{"vector", "ingest", "--delta"},
			} {
				env, output, warnings := newCronTestEnv()
				code, err := executeWithEnv(env, args, nil)
				if err == nil || code == ExitOK ||
					!strings.Contains(err.Error(), "cannot record a cron journey") ||
					!strings.Contains(err.Error(), "remedy: roca cron run nightly") {
					t.Fatalf("%v = code %d err %v out=%q errOut=%q",
						args, code, err, output.String(), warnings.String())
				}
			}
			db := filepath.Join(home, ".roca", "plugins", rocacron.Name, rocacron.DatabaseFilename)
			if _, err := os.Stat(db); !os.IsNotExist(err) {
				t.Fatalf("direct scheduler invocation recorded a journey database: %v", err)
			}
		})
	}
}

func TestObservedSchedulerInvocationIsNotRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_MODELS_ORDER", "none")
	t.Setenv(rocacron.ObservedEnv, "1")
	writeConfig(t, home, "[features]\ncron = true\n")
	previous := schedulerAncestry
	schedulerAncestry = func() []authorshipProcess {
		return []authorshipProcess{{Command: "cron"}}
	}
	t.Cleanup(func() { schedulerAncestry = previous })
	env, _, _ := newCronTestEnv()
	_, err := executeWithEnv(env, []string{"ingest"}, nil)
	if err != nil && strings.Contains(err.Error(), "cannot record a cron journey") {
		t.Fatalf("observed ingest was refused: %v", err)
	}
}

func TestCronRunRecordsAnObservedJourney(t *testing.T) {
	home := setupUnixCronRide(t, `command = 'printf %s "$ROCA_CRON_OBSERVED"'`)
	env, output, warnings := newCronTestEnv()
	code, err := executeWithEnv(env, []string{"cron", "run", "hourly"}, nil)
	if err != nil || code != ExitOK || !strings.Contains(output.String(), "exit=0") {
		t.Fatalf("hourly observed ride = code %d err %v out=%q errOut=%q",
			code, err, output.String(), warnings.String())
	}
	db, err := sql.Open("sqlite", filepath.Join(home, ".roca", "plugins", rocacron.Name, rocacron.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	var stdout string
	if err := db.QueryRow(`SELECT COUNT(*) FROM journeys`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT stdout FROM journeys`).Scan(&stdout); err != nil {
		t.Fatal(err)
	}
	if count != 1 || stdout != "1" {
		t.Fatalf("journeys = %d stdout = %q", count, stdout)
	}
}

func setupUnixCronRide(t *testing.T, command string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix shell ride")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROCA_MODELS_ORDER", "none")
	writeConfig(t, home, `[features]
cron = true

[ride.vector_delta]
train = "hourly"
`+command+"\n")
	ensureCronInstalled(t, home)
	return home
}
