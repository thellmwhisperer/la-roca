package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/hooks"
	"github.com/thellmwhisperer/la-roca/internal/service"
)

// EnvProject scopes a hook to one project without a flag, which is how a shell
// that already knows where it is passes it down.
const EnvProject = "ROCA_PROJECT"

// The session-lifecycle surface, job J3.
//
// Half of it wires the runtime up (`install`, `uninstall`, `status`) and half
// of it is the transport the runtime then runs (`context`, `handoff`,
// `record`). They are the same command because the settings file that declares
// these lines is the other half of the same contract.
//
// **Two exit-code rules, and they are contracts.** The transport commands never
// exit non-zero: they run on the critical path of somebody's session, and a hook
// that fails is a hook that can break it. Whatever went wrong is written to the
// error stream and the session carries on with less context. The wiring
// commands are ordinary tools and report failure with 1.
func hookCommand(env *cliEnv) *cobra.Command {
	hook := &cobra.Command{
		Use:   "hook",
		Short: "The session lifecycle: what a new session is handed, and what an ending one leaves",
	}
	hook.AddCommand(
		hookInstallCommand(env), hookUninstallCommand(env), hookStatusCommand(env),
		hookContextCommand(env), hookHandoffCommand(env), hookRecordCommand(env),
	)
	return hook
}

func hookInstallCommand(env *cliEnv) *cobra.Command {
	var settings, executable string
	cmd := &cobra.Command{
		Use:   "install <runtime>",
		Short: "Declare the lifecycle hooks in a runtime's settings file",
		Long: "Writes one command per lifecycle event into the runtime's own settings\n" +
			"file. Every hook that is not Roca's is left where it is, and so is every\n" +
			"byte outside the hooks member.\n\n" +
			"Supported runtimes: " + strings.Join(hooks.Runtimes(), ", "),
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := settingsFileOf(settings)
			if err != nil {
				return err
			}
			outcome, err := hooks.Install(args[0], path, chosenExecutable(executable))
			if err != nil {
				return err
			}
			return env.renderOutcome(outcome, "installed")
		},
	}
	cmd.Flags().StringVar(&settings, "settings", "", "the settings file to edit")
	cmd.Flags().StringVar(&executable, "executable", "",
		"the binary the hooks run (default `roca`, or "+EnvExecutable+")")
	return cmd
}

func hookUninstallCommand(env *cliEnv) *cobra.Command {
	var settings string
	cmd := &cobra.Command{
		Use:   "uninstall <runtime>",
		Short: "Withdraw the lifecycle hooks, leaving the rest of the file alone",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := settingsFileOf(settings)
			if err != nil {
				return err
			}
			outcome, err := hooks.Uninstall(args[0], path)
			if err != nil {
				return err
			}
			return env.renderOutcome(outcome, "withdrawn")
		},
	}
	cmd.Flags().StringVar(&settings, "settings", "", "the settings file to edit")
	return cmd
}

func hookStatusCommand(env *cliEnv) *cobra.Command {
	var settings string
	cmd := &cobra.Command{
		Use:   "status [runtime]",
		Short: "Which lifecycle events are declared, and where",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := settingsFileOf(settings)
			if err != nil {
				return err
			}
			return runtimeStatus(env, args, hooks.Runtimes(),
				func(runtime string) (hooks.Report, error) {
					return hooks.Status(runtime, path)
				},
				[]string{"runtime", "state", "detail"}, func(report hooks.Report) map[string]any {
					detail := strings.Join(report.Events, ", ")
					if detail == "" {
						detail = firstNonEmpty(report.Error, report.Path)
					}
					return map[string]any{"runtime": report.Runtime, "state": report.State, "detail": detail}
				},
				"Run `roca hook install <runtime>` to configure lifecycle context",
				"Run `roca hook uninstall <runtime>` to withdraw the hooks")
		},
	}
	cmd.Flags().StringVar(&settings, "settings", "", "the settings file to read")
	return cmd
}

func hookContextCommand(env *cliEnv) *cobra.Command {
	var scope hookScope
	var runtime string
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Print everything a fresh session should already know, under budget",
		Long: "The read half of the lifecycle contract. It answers one question per call\n" +
			"and always answers it under the injection budget, so a very long handoff\n" +
			"cannot flood somebody's context window.\n\n" +
			"With --runtime it speaks that runtime's own protocol; without it, plain text.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return env.emit(cmd, runtime, scope, func(
				svc *service.Service, req service.ContextRequest) (service.ContextAnswer, error) {
				return svc.SessionContext(cmd.Context(), req)
			})
		},
	}
	scope.bind(cmd)
	cmd.Flags().StringVar(&runtime, "runtime", "",
		"speak this runtime's protocol ("+strings.Join(hooks.Runtimes(), ", ")+")")
	return cmd
}

func hookHandoffCommand(env *cliEnv) *cobra.Command {
	var scope hookScope
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Print the most recent handoff, under budget",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return env.emit(cmd, "", scope, func(
				svc *service.Service, req service.ContextRequest) (service.ContextAnswer, error) {
				return svc.LatestHandoff(cmd.Context(), req)
			})
		},
	}
	scope.bind(cmd)
	return cmd
}

func hookRecordCommand(env *cliEnv) *cobra.Command {
	var req service.HandoffRequest
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record one handoff for a lifecycle trigger",
		Long: "The write half of the lifecycle contract. Silence means it landed.\n\n" +
			"The runtime's event payload is read from standard input when there is one,\n" +
			"so the session and the working directory come from the runtime itself and\n" +
			"not from what a settings file happened to hard-code.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			payload := readEventPayload(cmd.InOrStdin())
			req.Session = firstNonEmpty(req.Session, payload.Session)
			req.CWD = firstNonEmpty(req.CWD, payload.CWD)
			req.Project = firstNonEmpty(req.Project, os.Getenv(EnvProject))
			req.Surface = service.SurfaceCLI

			svc, _, err := env.openService()
			if err != nil {
				return env.degrade(err)
			}
			defer svc.Close()

			if _, err := svc.RecordHandoff(cmd.Context(), req); err != nil {
				return env.degrade(err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Trigger, "trigger", "",
		"the lifecycle moment being preserved ("+
			service.TriggerPreCompact+", "+service.TriggerSessionEnd+")")
	cmd.Flags().StringVar(&req.Session, "session-id", "", "the session that is ending")
	cmd.Flags().StringVar(&req.CWD, "cwd", "", "the working directory of that session")
	cmd.Flags().StringVar(&req.Project, "project", "", "project scope")
	cmd.MarkFlagRequired("trigger")
	return cmd
}

// hookScope is what every read shares: which project, and how much of the
// context window it is allowed to take.
type hookScope struct {
	project  string
	maxChars int
	roster   []string
	declared bool
}

func (s *hookScope) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&s.project, "project", "", "project scope")
	cmd.Flags().IntVar(&s.maxChars, "max-chars", 0,
		"the injection budget in characters (default "+
			fmt.Sprintf("%d", hooks.DefaultMaxChars)+", or "+hooks.EnvMaxChars+")")
	cmd.Flags().StringSliceVar(&s.roster, "pills", nil,
		"serve exactly these pills; declare it empty to serve none")
}

func (s hookScope) request(cmd *cobra.Command) service.ContextRequest {
	return service.ContextRequest{
		Project:        firstNonEmpty(s.project, os.Getenv(EnvProject)),
		MaxChars:       s.budget(),
		Roster:         s.roster,
		RosterDeclared: cmd.Flags().Changed("pills"),
	}
}

func (s hookScope) budget() int {
	if s.maxChars > 0 {
		return s.maxChars
	}
	return hooks.ResolveLimit(os.Getenv(hooks.EnvMaxChars), "")
}

// emit prints one budgeted read: the runtime's own protocol when it asked for
// it, JSON when somebody is measuring the budget, and plain text otherwise.
func (env *cliEnv) emit(cmd *cobra.Command, runtime string, scope hookScope,
	load func(*service.Service, service.ContextRequest) (service.ContextAnswer, error)) error {
	svc, _, err := env.openService()
	if err != nil {
		return env.degrade(err)
	}
	defer svc.Close()

	answer, err := load(svc, scope.request(cmd))
	if err != nil {
		return env.degrade(err)
	}
	if env.json {
		return env.printJSON(answer)
	}
	if runtime != "" {
		rendered, err := hooks.RenderSessionStart(answer.Context)
		if err != nil {
			return env.degrade(err)
		}
		if rendered != "" {
			env.print("%s", rendered)
		}
		return nil
	}
	if answer.Context != "" {
		env.print("%s", answer.Context)
	}
	return nil
}

// degrade is the transport's exit-code contract: a hook runs on the critical
// path of somebody's session, so whatever went wrong is written to the error
// stream and the session carries on with less context instead of breaking.
//
// A machine with no Roca on it is the common case, not a failure: an agent
// there stays silent rather than printing noise into every session.
func (env *cliEnv) degrade(err error) error {
	fmt.Fprintf(env.errOut, "roca hook: %v\n", err)
	env.code = ExitOK
	return nil
}

// eventPayload is what a runtime writes to the hook's standard input.
type eventPayload struct {
	Session string `json:"session_id"`
	CWD     string `json:"cwd"`
}

// readEventPayload reads it, degrading to nothing: the event still has to run
// when the payload is missing or unreadable.
func readEventPayload(in io.Reader) eventPayload {
	if in == nil {
		return eventPayload{}
	}
	raw, err := io.ReadAll(io.LimitReader(in, 1<<20))
	if err != nil || len(raw) == 0 {
		return eventPayload{}
	}
	var payload eventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return eventPayload{}
	}
	return payload
}

// settingsFileOf resolves the runtime's settings file, unless the operator
// named it themselves.
func settingsFileOf(declared string) (string, error) {
	if declared != "" {
		return declared, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf(
			"I do not know where your HOME is: name the file with --settings")
	}
	return hooks.SettingsPath(home, os.Getenv), nil
}
