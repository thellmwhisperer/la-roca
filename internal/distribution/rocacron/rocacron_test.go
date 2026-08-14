package rocacron_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func TestDryRunPreviewsOrderAndGateWithoutExecutingOrRecording(t *testing.T) {
	root, database := cronWorld(t, `[ride.vector_delta]
command = "roca vector ingest --delta"
gate = "after_ingest"
`)
	var invoked []string
	service := newService(t, root, database, func(_ context.Context, command string, _, _ io.Writer) (int, error) {
		invoked = append(invoked, command)
		return 0, nil
	})

	report, err := service.Run(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(invoked) != 0 || report.Train != plugin.DefaultTrain || len(report.Rides) != 2 {
		t.Fatalf("dry run = %+v invoked = %v", report, invoked)
	}
	if report.Rides[0].Plugin != "core" || report.Rides[0].Ride != "ingest" ||
		report.Rides[0].GateStatus != rocacron.GateReady {
		t.Fatalf("first preview = %+v", report.Rides[0])
	}
	if report.Rides[1].Plugin != "vector" ||
		report.Rides[1].GateStatus != rocacron.GateDeferredAfterIngest {
		t.Fatalf("second preview = %+v", report.Rides[1])
	}
	assertJourneyCount(t, database, 0)
}

func TestRunInvokesInOrderAndACompletedIngestOpensItsGate(t *testing.T) {
	root, database := cronWorld(t, `[ride.vector_delta]
command = "roca vector ingest --delta"
gate = "after_ingest"
`)
	var invoked []string
	service := newService(t, root, database, func(_ context.Context, command string, out, errOut io.Writer) (int, error) {
		invoked = append(invoked, command)
		fmt.Fprint(out, "observed stdout for "+command)
		fmt.Fprint(errOut, "observed stderr for "+command)
		return 0, nil
	})

	registered, _ := service.List()
	report, err := service.Run(context.Background(), plugin.DefaultTrain, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{registered[0].Command, "roca vector ingest --delta"}
	if !slices.Equal(invoked, want) || report.Failed != 0 || report.Deferred != 0 {
		t.Fatalf("run = %+v invoked = %v", report, invoked)
	}
	journeys := readJourneys(t, database)
	if len(journeys) != 2 || journeys[0].GateStatus != rocacron.GateReady ||
		journeys[1].GateStatus != rocacron.GateAfterIngestOK {
		t.Fatalf("journeys = %+v", journeys)
	}
	if !strings.Contains(journeys[1].Stdout, "observed stdout") ||
		!strings.Contains(journeys[1].Stderr, "observed stderr") {
		t.Fatalf("subprocess streams were not recorded: %+v", journeys[1])
	}
}

func TestFailedIngestIsRecordedAndDefersTheDependentRide(t *testing.T) {
	root, database := cronWorld(t, `[ride.vector_delta]
command = "roca vector ingest --delta"
gate = "after_ingest"
`)
	service := newService(t, root, database, func(_ context.Context, command string, _, errOut io.Writer) (int, error) {
		if strings.HasSuffix(command, " ingest") {
			fmt.Fprint(errOut, "synthetic ingest failure")
			return 7, fmt.Errorf("exit status 7")
		}
		return 0, nil
	})

	report, err := service.Run(context.Background(), plugin.DefaultTrain, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 || report.Deferred != 1 {
		t.Fatalf("report = %+v", report)
	}
	journeys := readJourneys(t, database)
	if len(journeys) != 2 || journeys[0].ExitCode == nil || *journeys[0].ExitCode != 7 ||
		journeys[0].Error == "" || journeys[1].ExitCode != nil ||
		journeys[1].GateStatus != rocacron.GateDeferredAfterIngest {
		t.Fatalf("journeys = %+v", journeys)
	}
}

func TestBusyCoreLockDefersWithoutInvokingAndRecordsTheDecision(t *testing.T) {
	root, database := cronWorld(t, "")
	service := newService(t, root, database, func(context.Context, string, io.Writer, io.Writer) (int, error) {
		t.Fatal("a locked train invoked a ride")
		return 0, nil
	})
	service.LockFree = func(string) (bool, error) { return false, nil }

	report, err := service.Run(context.Background(), plugin.DefaultTrain, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Deferred != 1 || report.Rides[0].GateStatus != rocacron.GateDeferredLocked {
		t.Fatalf("locked report = %+v", report)
	}
	journeys := readJourneys(t, database)
	if len(journeys) != 1 || journeys[0].ExitCode != nil ||
		journeys[0].GateStatus != rocacron.GateDeferredLocked {
		t.Fatalf("locked journeys = %+v", journeys)
	}
}

func TestListAggregatesCoreAndInstalledPluginRides(t *testing.T) {
	root, database := cronWorld(t, `[ride.vector_delta]
train = "hourly"
command = "roca vector ingest --delta"
`)
	service := newService(t, root, database, nil)
	rides, warnings := service.List()
	if len(warnings) != 0 || len(rides) != 2 {
		t.Fatalf("rides = %+v warnings = %v", rides, warnings)
	}
	if rides[0].Plugin != "core" || rides[1].Plugin != "vector" || rides[1].Train != "hourly" {
		t.Fatalf("rides = %+v", rides)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if want := "'" + binary + "' ingest"; runtime.GOOS != "windows" && rides[0].Command != want {
		t.Fatalf("core ride command = %q, want the absolute %q that system cron can find", rides[0].Command, want)
	}
}

func TestAGateReadsItsOwnPluginsRideBeforeASharedName(t *testing.T) {
	root, database := cronWorld(t, "")
	writeRides(t, root, "archive", `[ride.compact]
command = "roca archive compact"

[ride.prune]
train = "hourly"
command = "roca archive prune"
gate = "after_compact"
`)
	service := newService(t, root, database, func(context.Context, string, io.Writer, io.Writer) (int, error) {
		return 0, nil
	})
	if _, err := service.Run(context.Background(), plugin.DefaultTrain, false); err != nil {
		t.Fatal(err)
	}
	insertJourney(t, database, "vector", "compact", 4)

	report, err := service.Run(context.Background(), "hourly", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rides) != 1 || report.Rides[0].GateStatus != "after_compact_ok" || !report.Rides[0].Executed {
		t.Fatalf("another plugin's compact decided this gate: %+v", report.Rides)
	}
}

func TestARideTheTrainCannotObserveDoesNotStopTheRidesBehindIt(t *testing.T) {
	root, database := cronWorld(t, `[ride.vector_delta]
command = "roca vector ingest --delta"
`)
	service := newService(t, root, database, func(context.Context, string, io.Writer, io.Writer) (int, error) {
		return 0, nil
	})
	service.LockFree = func(string) (bool, error) { return false, errors.New("the lock is unreadable") }

	report, err := service.Run(context.Background(), plugin.DefaultTrain, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rides) != 2 || report.Failed != 2 || len(report.Warnings) != 2 {
		t.Fatalf("an unobservable first ride cancelled the train: %+v", report)
	}
	for _, ride := range report.Rides {
		if ride.GateStatus != rocacron.GateUnobserved || ride.Error == "" {
			t.Fatalf("ride = %+v", ride)
		}
	}
}

func TestRecordedStreamsAreBoundedAndRedacted(t *testing.T) {
	root, database := cronWorld(t, "")
	secret := "ghp_" + strings.Repeat("s3cr3t", 4)
	service := newService(t, root, database, func(_ context.Context, _ string, out, errOut io.Writer) (int, error) {
		fmt.Fprint(out, strings.Repeat("chatter ", 40_000))
		fmt.Fprint(errOut, "the ride echoed "+secret+" out loud")
		return 0, nil
	})

	if _, err := service.Run(context.Background(), plugin.DefaultTrain, false); err != nil {
		t.Fatal(err)
	}
	journeys := readJourneys(t, database)
	if len(journeys) != 1 {
		t.Fatalf("journeys = %+v", journeys)
	}
	if size := len(journeys[0].Stdout); size > (64<<10)+64 {
		t.Errorf("recorded stdout = %d bytes, want a bounded excerpt", size)
	}
	if !strings.Contains(journeys[0].Stdout, "bytes were not kept") {
		t.Errorf("a truncated stream did not say so: %q", journeys[0].Stdout[max(0, len(journeys[0].Stdout)-80):])
	}
	if strings.Contains(journeys[0].Stderr, secret) {
		t.Errorf("the journey kept a credential: %q", journeys[0].Stderr)
	}
}

func TestReadOnlyDryRunDoesNotCreateJourneyState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	database := filepath.Join(root, rocacron.Name, rocacron.DatabaseFilename)
	service, err := rocacron.Open(rocacron.Options{
		PluginRoot: root,
		Database:   database,
		LockPath:   filepath.Join(t.TempDir(), ".roca.lock"),
		ReadOnly:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	report, err := service.Run(context.Background(), plugin.DefaultTrain, true)
	if err != nil || len(report.Rides) != 1 || report.Rides[0].GateStatus != rocacron.GateReady {
		t.Fatalf("read-only dry run = %+v, %v", report, err)
	}
	if _, err := os.Stat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only dry run created a database: %v", err)
	}
}

// The other lock scenarios inject a decision; this one exercises the real
// probe against the core writer, because the train is only an observer if it
// leaves the lock exactly as it found it: absent when absent, free afterwards.
func TestTheRealCoreLockProbeNeitherCreatesNorKeepsTheLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	core := logfile.New(t.TempDir())
	// The log directory exists before any train runs, so an absent lock file
	// there is the probe's own restraint rather than a missing parent.
	if err := os.MkdirAll(filepath.Dir(core.LockPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := rocacron.Open(rocacron.Options{
		PluginRoot: root,
		Database:   filepath.Join(t.TempDir(), rocacron.DatabaseFilename),
		LockPath:   core.LockPath(),
		RunCommand: func(context.Context, string, io.Writer, io.Writer) (int, error) { return 0, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	preview := func() string {
		t.Helper()
		report, err := service.Run(context.Background(), plugin.DefaultTrain, true)
		if err != nil || len(report.Rides) != 1 {
			t.Fatalf("preview = %+v, %v", report, err)
		}
		return report.Rides[0].GateStatus
	}

	if status := preview(); status != rocacron.GateReady {
		t.Fatalf("an absent core lock previewed %q", status)
	}
	if _, err := os.Stat(core.LockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the probe created the core lock: %v", err)
	}

	release, err := core.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if status := preview(); status != rocacron.GateDeferredLocked {
		t.Fatalf("a busy core lock previewed %q", status)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	// The second of these two previews is the one that matters: a probe that
	// kept what the first took would collide with itself, because a second
	// descriptor cannot flock the file the first one still holds.
	for _, status := range []string{preview(), preview()} {
		if status != rocacron.GateReady {
			t.Fatalf("a released core lock previewed %q", status)
		}
	}
}

func cronWorld(t *testing.T, vectorRides string) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugins")
	if vectorRides != "" {
		writeRides(t, root, "vector", vectorRides)
	}
	return root, filepath.Join(t.TempDir(), rocacron.DatabaseFilename)
}

func writeRides(t *testing.T, root, pluginName, manifest string) {
	t.Helper()
	directory := filepath.Join(root, pluginName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, plugin.RidesFilename), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func insertJourney(t *testing.T, path, pluginName, ride string, exitCode int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	when := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO journeys
		(train, ride, plugin, started_at, ended_at, duration_ms, exit_code, gate_status)
		VALUES ('nightly', ?, ?, ?, ?, 1, ?, 'ready')`, ride, pluginName, when, when, exitCode); err != nil {
		t.Fatal(err)
	}
}

func newService(t *testing.T, root, database string, runner rocacron.CommandRunner) *rocacron.Service {
	t.Helper()
	clock := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	service, err := rocacron.Open(rocacron.Options{
		PluginRoot: root,
		Database:   database,
		LockPath:   filepath.Join(t.TempDir(), ".roca.lock"),
		Now: func() time.Time {
			clock = clock.Add(25 * time.Millisecond)
			return clock
		},
		RunCommand: runner,
		Out:        io.Discard,
		ErrOut:     io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

type journey struct {
	GateStatus string
	ExitCode   *int
	Error      string
	Stdout     string
	Stderr     string
}

func readJourneys(t *testing.T, path string) []journey {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT gate_status, exit_code, error, stdout, stderr FROM journeys ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var journeys []journey
	for rows.Next() {
		var got journey
		if err := rows.Scan(&got.GateStatus, &got.ExitCode, &got.Error, &got.Stdout, &got.Stderr); err != nil {
			t.Fatal(err)
		}
		journeys = append(journeys, got)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return journeys
}

func assertJourneyCount(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM journeys").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("journey count = %d, want %d", got, want)
	}
}
