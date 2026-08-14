package rocacron_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

	report, err := service.Run(context.Background(), plugin.DefaultTrain, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"roca ingest", "roca vector ingest --delta"}
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
		if command == "roca ingest" {
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

func cronWorld(t *testing.T, vectorRides string) (string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugins")
	if vectorRides != "" {
		directory := filepath.Join(root, "vector")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, plugin.RidesFilename), []byte(vectorRides), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(t.TempDir(), rocacron.DatabaseFilename)
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
