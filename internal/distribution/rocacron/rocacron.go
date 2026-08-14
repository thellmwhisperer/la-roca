// Package rocacron owns the train that invokes plugin rides and records their
// journeys without taking ownership of the core lock or of the work itself.
package rocacron

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

const (
	Name             = "roca-cron"
	DatabaseFilename = "roca-cron.db"

	GateReady               = "ready"
	GateAfterIngestOK       = "after_ingest_ok"
	GateDeferredAfterIngest = "deferred_after_ingest"
	GateDeferredLocked      = "deferred_locked"
)

//go:embed schema.sql
var schema string

type CommandRunner func(context.Context, string, io.Writer, io.Writer) (int, error)

type Options struct {
	PluginRoot string
	Database   string
	LockPath   string
	ReadOnly   bool
	Now        func() time.Time
	RunCommand CommandRunner
	Out        io.Writer
	ErrOut     io.Writer
}

type Service struct {
	db         *sql.DB
	pluginRoot string
	lockPath   string
	now        func() time.Time
	runCommand CommandRunner
	out        io.Writer
	errOut     io.Writer
	readOnly   bool
	LockFree   func(string) (bool, error)
}

type RideResult struct {
	Plugin     string `json:"plugin"`
	Ride       string `json:"ride"`
	Train      string `json:"train"`
	Command    string `json:"command"`
	Gate       string `json:"gate,omitempty"`
	GateStatus string `json:"gate_status"`
	Executed   bool   `json:"executed"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type Report struct {
	Train    string       `json:"train"`
	DryRun   bool         `json:"dry_run"`
	Rides    []RideResult `json:"rides"`
	Warnings []string     `json:"warnings,omitempty"`
	Failed   int          `json:"failed"`
	Deferred int          `json:"deferred"`
}

func Open(options Options) (*Service, error) {
	if strings.TrimSpace(options.Database) == "" {
		return nil, fmt.Errorf("the journey database is not configured")
	}
	var db *sql.DB
	if options.ReadOnly {
		var err error
		db, err = openExistingDatabase(options.Database)
		if err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(options.Database), 0o700); err != nil {
			return nil, fmt.Errorf("create the journey database directory: %w", err)
		}
		var err error
		db, err = openDatabase(options.Database, false)
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(schema); err != nil {
			db.Close()
			return nil, fmt.Errorf("apply the %s schema: %w", Name, err)
		}
		if err := os.Chmod(options.Database, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("protect the journey database: %w", err)
		}
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}
	runner := options.RunCommand
	if runner == nil {
		runner = runShellCommand
	}
	out, errOut := options.Out, options.ErrOut
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	return &Service{
		db: db, pluginRoot: options.PluginRoot, lockPath: options.LockPath,
		now: now, runCommand: runner, out: out, errOut: errOut,
		readOnly: options.ReadOnly, LockFree: coreLockFree,
	}, nil
}

func openExistingDatabase(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect the journey database: %w", err)
	}
	db, err := openDatabase(path, true)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open the journey database read-only: %w", err)
	}
	return db, nil
}

func openDatabase(path string, readOnly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve the journey database: %w", err)
	}
	query := url.Values{"_pragma": {"busy_timeout(15000)"}}
	if readOnly {
		query.Set("mode", "ro")
	}
	dsn := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute), RawQuery: query.Encode()}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open the journey database: %w", err)
	}
	return db, nil
}

func (s *Service) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Service) List() ([]plugin.Ride, []string) {
	discovered, warnings := plugin.DiscoverRides(s.pluginRoot)
	return append([]plugin.Ride{coreIngestRide()}, discovered...), warnings
}

func coreIngestRide() plugin.Ride {
	return plugin.Ride{
		Name: "ingest", Plugin: "core", Train: plugin.DefaultTrain, Command: "roca ingest",
	}
}

func (s *Service) Run(ctx context.Context, train string, dryRun bool) (Report, error) {
	if s.readOnly && !dryRun {
		return Report{}, fmt.Errorf("the read-only journey observer cannot run a train")
	}
	train = strings.TrimSpace(train)
	if train == "" {
		train = plugin.DefaultTrain
	}
	all, warnings := s.List()
	report := Report{Train: train, DryRun: dryRun, Warnings: warnings}
	for _, ride := range all {
		if ride.Train != train {
			continue
		}
		result, err := s.runRide(ctx, ride, dryRun)
		if err != nil {
			return report, err
		}
		report.Rides = append(report.Rides, result)
		if dryRun {
			if strings.HasPrefix(result.GateStatus, "deferred_") {
				report.Deferred++
			}
		} else if !result.Executed {
			report.Deferred++
		} else if result.ExitCode != nil && *result.ExitCode != 0 {
			report.Failed++
		}
	}
	if len(report.Rides) == 0 {
		return report, fmt.Errorf("train %q has no registered rides", train)
	}
	return report, nil
}

func (s *Service) runRide(ctx context.Context, ride plugin.Ride, dryRun bool) (RideResult, error) {
	result := RideResult{
		Plugin: ride.Plugin, Ride: ride.Name, Train: ride.Train,
		Command: ride.Command, Gate: ride.Gate,
	}
	free, err := s.LockFree(s.lockPath)
	if err != nil {
		return result, fmt.Errorf("check the core lock: %w", err)
	}
	if !free {
		result.GateStatus = GateDeferredLocked
		if dryRun {
			return result, nil
		}
		return result, s.recordDeferred(ride, result.GateStatus, "the core lock is busy")
	}
	status, open, err := s.gateStatus(ctx, ride.Gate)
	if err != nil {
		return result, err
	}
	result.GateStatus = status
	if dryRun {
		return result, nil
	}
	if !open {
		return result, s.recordDeferred(ride, status, "the gated dependency has no latest successful journey")
	}

	started := s.now().UTC()
	var stdout, stderr bytes.Buffer
	exitCode, runErr := s.runCommand(ctx, ride.Command,
		io.MultiWriter(s.out, &stdout), io.MultiWriter(s.errOut, &stderr))
	ended := s.now().UTC()
	result.Executed = true
	result.ExitCode = &exitCode
	result.DurationMS = max(0, ended.Sub(started).Milliseconds())
	journey := journeyRecord{
		Ride: ride, Started: started, Ended: ended, DurationMS: result.DurationMS,
		ExitCode: &exitCode, GateStatus: status, Stdout: stdout.String(), Stderr: stderr.String(),
	}
	if runErr != nil {
		journey.Error = runErr.Error()
	} else if exitCode != 0 {
		journey.Error = fmt.Sprintf("exit status %d", exitCode)
	}
	if err := s.record(journey); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) gateStatus(ctx context.Context, gate string) (string, bool, error) {
	if gate == "" {
		return GateReady, true, nil
	}
	dependency, found := strings.CutPrefix(gate, "after_")
	if !found || dependency == "" {
		return "deferred_" + gate, false, nil
	}
	ok, err := s.lastJourneyOK(ctx, dependency)
	if err != nil {
		return "", false, err
	}
	if ok {
		return gate + "_ok", true, nil
	}
	return "deferred_" + gate, false, nil
}

func (s *Service) lastJourneyOK(ctx context.Context, ride string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var exitCode *int
	var err error
	if ride == "ingest" {
		err = s.db.QueryRowContext(ctx, `SELECT exit_code FROM journeys
			WHERE ride = ? AND plugin = 'core' ORDER BY id DESC LIMIT 1`, ride).Scan(&exitCode)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT exit_code FROM journeys
			WHERE ride = ? ORDER BY id DESC LIMIT 1`, ride).Scan(&exitCode)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the last %s journey: %w", ride, err)
	}
	return exitCode != nil && *exitCode == 0, nil
}

func (s *Service) recordDeferred(ride plugin.Ride, status, reason string) error {
	when := s.now().UTC()
	return s.record(journeyRecord{
		Ride: ride, Started: when, Ended: when, GateStatus: status, Error: reason,
	})
}

type journeyRecord struct {
	Ride       plugin.Ride
	Started    time.Time
	Ended      time.Time
	DurationMS int64
	ExitCode   *int
	Error      string
	GateStatus string
	Stdout     string
	Stderr     string
}

func (s *Service) record(journey journeyRecord) error {
	if s.readOnly || s.db == nil {
		return fmt.Errorf("the read-only journey observer cannot record a journey")
	}
	_, err := s.db.Exec(`INSERT INTO journeys
		(train, ride, plugin, started_at, ended_at, duration_ms, exit_code, error, gate_status, stdout, stderr)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		journey.Ride.Train, journey.Ride.Name, journey.Ride.Plugin,
		journey.Started.Format(time.RFC3339Nano), journey.Ended.Format(time.RFC3339Nano),
		journey.DurationMS, journey.ExitCode, journey.Error, journey.GateStatus,
		journey.Stdout, journey.Stderr)
	if err != nil {
		return fmt.Errorf("record the %s/%s journey: %w", journey.Ride.Plugin, journey.Ride.Name, err)
	}
	return nil
}

func runShellCommand(ctx context.Context, command string, out, errOut io.Writer) (int, error) {
	var child *exec.Cmd
	if runtime.GOOS == "windows" {
		shell := os.Getenv("COMSPEC")
		if shell == "" {
			shell = "cmd.exe"
		}
		child = exec.CommandContext(ctx, shell, "/S", "/C", command)
	} else {
		child = exec.CommandContext(ctx, "/bin/sh", "-c", command)
	}
	child.Stdout, child.Stderr = out, errOut
	err := child.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), err
	}
	return -1, err
}
