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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	Name             = "roca-cron"
	DatabaseFilename = "roca-cron.db"

	GateReady               = "ready"
	GateAfterIngestOK       = "after_ingest_ok"
	GateDeferredAfterIngest = "deferred_after_ingest"
	GateDeferredLocked      = "deferred_locked"
	GateUnobserved          = "unobserved"

	corePlugin = "core"

	// A journey keeps a bounded, redacted excerpt of what it observed: the
	// streams belong to somebody else's command and are queryable afterwards.
	maxStreamBytes = 64 << 10
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
	Error      string `json:"error,omitempty"`
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
		db, err = bundledplugin.OpenDatabase(options.Database, false)
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
	db, err := bundledplugin.OpenDatabase(path, true)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open the journey database read-only: %w", err)
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
	discovered, warnings := plugin.DiscoverRides(s.pluginRoot, verifyInstalledRides)
	return append([]plugin.Ride{coreIngestRide()}, discovered...), warnings
}

func verifyInstalledRides(pluginName, directory string) error {
	if pluginName == corePlugin {
		return fmt.Errorf("plugin name %q is reserved for the built-in ride namespace", corePlugin)
	}
	_, err := plugininstall.VerifyInstalledPayload(pluginName, directory)
	return err
}

func coreIngestRide() plugin.Ride {
	return plugin.Ride{
		Name: "ingest", Plugin: corePlugin, Train: plugin.DefaultTrain,
		Command: coreCommand("ingest"),
	}
}

// coreCommand addresses the running binary by its own absolute path. System
// cron runs with a minimal PATH that does not contain the default install
// prefix, so a bare name would fail the nightly ride with exit 127.
func coreCommand(subcommand string) string {
	executable, err := os.Executable()
	if err != nil {
		return "roca " + subcommand
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return "roca " + subcommand
	}
	return shellQuote(absolute) + " " + subcommand
}

func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
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
	declared := make(map[string]bool, len(all))
	for _, ride := range all {
		declared[rideKey(ride.Plugin, ride.Name)] = true
	}
	report := Report{Train: train, DryRun: dryRun, Warnings: warnings}
	for _, ride := range all {
		if ride.Train != train {
			continue
		}
		result, err := s.runRide(ctx, ride, declared, dryRun)
		if err != nil {
			// One ride the train could not observe or record is that ride's
			// verdict, not the train's: the rides behind it are unrelated and
			// still deserve their trip.
			if result.GateStatus == "" {
				result.GateStatus = GateUnobserved
			}
			result.Error = err.Error()
			report.Warnings = append(report.Warnings,
				fmt.Sprintf("ride %s/%s: %v", ride.Plugin, ride.Name, err))
			report.Rides = append(report.Rides, result)
			report.Failed++
			continue
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

func (s *Service) runRide(ctx context.Context, ride plugin.Ride, declared map[string]bool, dryRun bool) (RideResult, error) {
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
		return result, s.recordDeferred(ride, result.GateStatus, "the core log lock is busy")
	}
	status, open, err := s.gateStatus(ctx, ride, declared)
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
	var stdout, stderr excerpt
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

func (s *Service) gateStatus(ctx context.Context, ride plugin.Ride, declared map[string]bool) (string, bool, error) {
	if ride.Gate == "" {
		return GateReady, true, nil
	}
	dependency, found := strings.CutPrefix(ride.Gate, "after_")
	if !found || dependency == "" {
		return "deferred_" + ride.Gate, false, nil
	}
	owner, resolved := gateOwner(ride, dependency, declared)
	if !resolved {
		return "", false, fmt.Errorf(
			"ride %s/%s gate %s has no declared dependency", ride.Plugin, ride.Name, ride.Gate)
	}
	ok, err := s.lastJourneyOK(ctx, owner, dependency)
	if err != nil {
		return "", false, err
	}
	if ok {
		return ride.Gate + "_ok", true, nil
	}
	return "deferred_" + ride.Gate, false, nil
}

// gateOwner keeps a gate inside its own plugin. Core ingest is the sole
// cross-plugin exception; every other absent dependency is unresolved rather
// than a core journey that can never exist.
func gateOwner(ride plugin.Ride, dependency string, declared map[string]bool) (string, bool) {
	if declared[rideKey(ride.Plugin, dependency)] {
		return ride.Plugin, true
	}
	if dependency == "ingest" && declared[rideKey(corePlugin, dependency)] {
		return corePlugin, true
	}
	return "", false
}

func rideKey(pluginName, ride string) string { return pluginName + "\x00" + ride }

func (s *Service) lastJourneyOK(ctx context.Context, owner, ride string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	var exitCode *int
	err := s.db.QueryRowContext(ctx, `SELECT exit_code FROM journeys
		WHERE ride = ? AND plugin = ? ORDER BY id DESC LIMIT 1`, ride, owner).Scan(&exitCode)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the last %s/%s journey: %w", owner, ride, err)
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

// excerpt keeps the head of an observed stream and counts the rest away, so a
// chatty ride can neither exhaust memory nor grow the journey database without
// bound. The invoked command still writes its whole stream to the real output.
type excerpt struct {
	head    bytes.Buffer
	dropped int
}

func (e *excerpt) Write(payload []byte) (int, error) {
	if room := maxStreamBytes - e.head.Len(); room > 0 {
		if len(payload) <= room {
			e.head.Write(payload)
			return len(payload), nil
		}
		e.head.Write(payload[:room])
		e.dropped += len(payload) - room
		return len(payload), nil
	}
	e.dropped += len(payload)
	return len(payload), nil
}

// String redacts before it stores: a ride's diagnostic can echo a credential
// and the journeys table is queryable, so it holds the same shape of secret
// the operational log already refuses to keep.
func (e *excerpt) String() string {
	text := strings.ToValidUTF8(e.head.String(), "")
	redacted, ok := logfile.Redact(text).(string)
	if !ok {
		redacted = "[REDACTED]"
	}
	if e.dropped > 0 {
		redacted += fmt.Sprintf("\n[%d more bytes were not kept]", e.dropped)
	}
	return redacted
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
