package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/toolcallobserver"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const (
	observerFollowEnv  = "ROCA_TOOL_CALL_OBSERVER_FOLLOW"
	observerHarnessEnv = "ROCA_TOOL_CALL_OBSERVER_HARNESS"
	observerKindEnv    = "ROCA_TOOL_CALL_OBSERVER_KIND"
)

func toolCallObserverCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "tool-call-observer",
		Short: "Live tool-call tail of this agent session",
		Long: "Opens a live window of the tool calls this agent session is making.\n" +
			"With Herdr, a new tab in the operator's designated human-facing workspace; without Herdr, a system terminal.",
		Args: cobra.NoArgs,
		RunE: env.runToolCallObserver,
	}
}

func (env *cliEnv) runToolCallObserver(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if path := strings.TrimSpace(os.Getenv(observerFollowEnv)); path != "" {
		return env.followObserver(ctx, path, os.Getenv(observerHarnessEnv), parsers.Kind(os.Getenv(observerKindEnv)))
	}
	session, err := env.resolveObserverSession()
	if err != nil {
		return err
	}
	if !parsers.Observable(session.Kind) {
		return fmt.Errorf("cannot watch this session: %s stores this session as a database, not a live log the observer can tail",
			toolcallobserver.ProductName(session.Harness))
	}
	opened, err := env.openObserverWindow(ctx, session)
	if err != nil {
		return err
	}
	document := map[string]any{
		"harness":      session.Harness,
		"session_id":   session.ID,
		"session_file": session.Path,
		"window":       opened,
	}
	if env.json {
		return env.printJSON(document)
	}
	env.print("observer: %s", toolcallobserver.ProductName(session.Harness))
	env.print("window: %s", windowLine(opened))
	env.print("%s", axi.RenderHelp(
		"Close the window or press Ctrl+C to stop watching",
		"Run `roca tool-call-observer --json` for the complete result envelope",
	))
	return nil
}

func (env *cliEnv) followObserver(ctx context.Context, path, harness string, kind parsers.Kind) error {
	follow := env.observerFollow
	if follow == nil {
		follow = toolcallobserver.Follow
	}
	err := follow(ctx, toolcallobserver.Session{
		Harness: harness, Kind: kind, Path: path, ID: filepath.Base(path),
	}, env.out, toolcallobserver.FollowOptions{})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (env *cliEnv) openObserverWindow(ctx context.Context, session toolcallobserver.Session) (toolcallobserver.OpenedWindow, error) {
	executable := env.observerExecutable
	if executable == "" {
		path, err := os.Executable()
		if err != nil {
			return toolcallobserver.OpenedWindow{}, fmt.Errorf("cannot locate the roca executable: %w", err)
		}
		executable = path
	}
	req := toolcallobserver.WindowRequest{
		LookPath:     env.observerLookPath,
		Runner:       env.observerRunner,
		OpenTerminal: env.observerTerminal,
		Command:      []string{executable, "tool-call-observer"},
		Env: []string{
			observerFollowEnv + "=" + session.Path,
			observerHarnessEnv + "=" + session.Harness,
			observerKindEnv + "=" + string(session.Kind),
		},
		CurrentID: os.Getenv("HERDR_WORKSPACE_ID"),
	}
	open := env.observerOpen
	if open == nil {
		open = toolcallobserver.OpenWindow
	}
	return open(ctx, session, req)
}

func (env *cliEnv) resolveObserverSession() (toolcallobserver.Session, error) {
	if env.observerResolve != nil {
		return env.observerResolve()
	}
	return toolcallobserver.Resolve(env.observerEvidence())
}

func (env *cliEnv) observerEvidence() toolcallobserver.Evidence {
	if env.observerLive != nil {
		return env.observerLive()
	}
	return liveObserverEvidence()
}

func liveObserverEvidence() toolcallobserver.Evidence {
	environment := map[string]string{}
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		environment[key] = value
	}
	processes := observerProcessAncestry(os.Getppid())
	home, _ := os.UserHomeDir()
	settings := ingest.Settings{}
	if environment["CLAUDE_PROJECTS_ROOT"] == "" {
		if dir := strings.TrimSpace(environment["CLAUDE_CONFIG_DIR"]); dir != "" {
			settings.ClaudeProjects = filepath.Join(dir, "projects")
		}
	}
	if environment["GROK_SESSIONS_ROOT"] == "" {
		if dir := strings.TrimSpace(environment["GROK_HOME"]); dir != "" {
			settings.GrokSessions = filepath.Join(dir, "sessions")
		}
	}
	return toolcallobserver.Evidence{
		Processes:   processes,
		Environment: environment,
		Roots: ingest.ResolveRoots(ingest.Environment{
			GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv,
		}, settings),
	}
}

func observerProcessAncestry(pid int) []toolcallobserver.Process {
	processes := make([]toolcallobserver.Process, 0, 8)
	for range 8 {
		output, err := exec.Command("ps", "-o", "ppid=", "-o", "comm=", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		fields := strings.Fields(string(output))
		if len(fields) < 2 {
			break
		}
		parent, err := strconv.Atoi(fields[0])
		if err != nil {
			break
		}
		processes = append(processes, toolcallobserver.Process{
			Command:   fields[1],
			Arguments: fields[2:],
			OpenFiles: openFilesOf(pid),
		})
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return processes
}

func windowLine(opened toolcallobserver.OpenedWindow) string {
	if opened.Kind == toolcallobserver.WindowHerdr {
		where := opened.Workspace
		if where == "" {
			where = "current"
		}
		return "new tab in workspace " + where
	}
	return "system terminal"
}
