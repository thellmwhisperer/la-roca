package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"

	"github.com/thellmwhisperer/la-roca/internal/distribution/supportreport"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
	resident "github.com/thellmwhisperer/la-roca/pkg/resident"
)

const (
	doctorQueryFailureWindow = 24 * time.Hour
	doctorQueryFailureLimit  = 5
)

type doctorReport struct {
	service.DoctorReport
	QueryFailures     logfile.QueryFailureSummary `json:"query_failures"`
	Vector            *vectorDoctorReport         `json:"vector,omitempty"`
	ReadOnlySnapshots leftoverSnapshots           `json:"read_only_snapshots"`
	ForeignOwned      []stateOwnership            `json:"foreign_owned,omitempty"`
}

type stateOwnership struct {
	Path    string `json:"path"`
	Owner   string `json:"owner"`
	Command string `json:"chown"`
}

func doctorCommand(env *cliEnv) *cobra.Command {
	var support bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the configuration and which model is going to answer",
		Long: "Reports where the data and the configuration are, which providers this\n" +
			"installation declares, which of them are available and, for the ones that\n" +
			"are not, the exact command that fixes it. Local agent models authenticate\n" +
			"through their own CLIs; La Roca stores no secrets.\n\n" +
			"`roca doctor --report` writes a privacy-safe support snapshot for pasting\n" +
			"into a chat or issue. It is read-only: it never installs plugins, never\n" +
			"adopts schema, and never changes the serving marker. Interactive doctor\n" +
			"also reports leftover read-only snapshot copies under the temp root and\n" +
			"offers to delete the abandoned ones.",
		RunE: func(cmd *cobra.Command, args []string) error {
			foreignOwned := env.collectForeignOwnedState()
			if support {
				env.skipExecutionLog = true
				err := env.runDoctorReport(cmd.Context(), foreignOwned)
				if err != nil && len(foreignOwned) > 0 {
					renderForeignOwnedSupport(env, len(foreignOwned))
				}
				return err
			}
			err := env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
				report, err := svc.Doctor(cmd.Context())
				if err != nil {
					return err
				}
				proposals, err := env.openCapabilityProposals()
				if err != nil {
					return err
				}
				for _, proposal := range proposals {
					report.CapabilityProposals = append(report.CapabilityProposals, proposal.Proposal.Alert)
				}
				audit := logfile.New(svc.DataDir())
				failures, logErr := audit.RecentQueryFailures(
					time.Now(), doctorQueryFailureWindow, doctorQueryFailureLimit)
				if logErr != nil {
					report.Warnings = append(report.Warnings,
						"query failure log could not be read: "+logErr.Error())
				}
				answer := doctorReport{DoctorReport: report, QueryFailures: failures,
					Vector:            env.collectVectorDoctor(cmd.Context()),
					ReadOnlySnapshots: collectSnapshotDoctor(),
					ForeignOwned:      foreignOwned}
				if env.json {
					return env.printJSON(answer)
				}
				renderDoctor(env, report)
				renderVectorDoctor(env, answer.Vector)
				renderSnapshotDoctor(env, answer.ReadOnlySnapshots)
				renderForeignOwnedState(env, answer.ForeignOwned)
				renderQueryFailures(env, failures)
				if err := env.offerSnapshotCleanup(cmd, answer.ReadOnlySnapshots); err != nil {
					return err
				}
				if terminalInput(cmd.InOrStdin()) && !env.skipReconciliation {
					_, err = env.reconcileCapabilities(cmd, true, true)
				}
				return err
			})(cmd, args)
			if err != nil && len(foreignOwned) > 0 {
				renderForeignOwnedStateTo(env.errOut, foreignOwned)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&support, "report", false,
		"emit a privacy-safe support snapshot for pasting into a chat or issue")
	return cmd
}

type doctorSupportReport struct {
	supportreport.Snapshot
	ForeignOwnedCount int `json:"foreign_owned_count,omitempty"`
}

func (env *cliEnv) runDoctorReport(ctx context.Context, foreignOwned []stateOwnership) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	snapshot, err := supportreport.Collect(ctx, supportreport.Options{
		Version: env.build.Version, Commit: env.build.Commit, Paths: paths, File: file,
		Home: home, PluginRoot: pluginRoot(paths), Prefix: os.Getenv(envRocaPrefix),
		Sources: ingestSources(file, home, paths.Runner),
	})
	if err != nil {
		return err
	}
	if env.json {
		return env.printJSON(doctorSupportReport{Snapshot: snapshot, ForeignOwnedCount: len(foreignOwned)})
	}
	env.print("%s", supportreport.Render(snapshot))
	renderForeignOwnedSupport(env, len(foreignOwned))
	return nil
}

func renderForeignOwnedSupport(env *cliEnv, count int) {
	if count == 0 {
		return
	}
	env.print("state ownership findings: %d (run `roca doctor` locally for exact repair commands)", count)
}

func renderQueryFailures(env *cliEnv, summary logfile.QueryFailureSummary) {
	env.print("query failures (last 24h): %d", summary.Count)
	if len(summary.Recent) == 0 {
		return
	}
	rows := make([]map[string]any, 0, len(summary.Recent))
	for _, failure := range summary.Recent {
		rows = append(rows, map[string]any{
			"time":   failure.Timestamp.UTC().Format(time.RFC3339),
			"source": failure.Source, "call": failure.Operation,
			"type": failure.ErrorType, "error": failure.Error,
			"correlation_id": failure.CorrelationID,
		})
	}
	env.print("%s", rowOutput(
		[]string{"time", "source", "call", "type", "error", "correlation_id"}, rows))
	if queryFailureTimedOut(summary) {
		env.print("Run roca vector query for the semantic leg alone; raise query.timeout_ms if the hybrid path is required")
	}
}

func queryFailureTimedOut(summary logfile.QueryFailureSummary) bool {
	for _, failure := range summary.Recent {
		text := strings.ToLower(failure.Error + " " + failure.ErrorType)
		if strings.Contains(text, "time limit") || strings.Contains(text, "timeout") ||
			strings.Contains(text, "exceeded") {
			return true
		}
	}
	return false
}

func renderDoctor(env *cliEnv, report service.DoctorReport) {
	env.print("roca %s (%s)", report.Version, report.SourceSHA)
	env.print("database: %s · %d memories", report.DBPath, report.Memories)
	renderBedrock(env, report.Bedrock)
	if report.ConfigExists {
		env.print("configuration: %s", report.ConfigPath)
	} else {
		env.print("configuration: %s (does not exist: defaults in use)", report.ConfigPath)
	}
	env.print("%s", renderQueryKnobs(report.Query))
	renderResidentDoctor(env, report.Resident)
	env.print("agents detected: %s", detectedAgentsLine(report.DetectedAgents))
	env.print("agents not found: %s", missingAgentsLine(report.DetectedAgents))
	env.print("authentication: local agent models use their own CLI sessions; La Roca stores no secrets")

	for _, warning := range report.Warnings {
		env.print("warning: %s", warning)
	}
	if len(report.LayerRepairs) > 0 {
		env.print("runtime_layers_not_in_registry: failed")
		for _, command := range report.LayerRepairs {
			env.print("      remedy: run `%s`", command)
		}
	}

	if report.ProviderNarration != "" {
		env.print("%s", report.ProviderNarration)
	} else {
		env.print("playground: optional plugin not installed or no provider declared")
	}

	if report.PromptPath != "" {
		if report.PromptExists {
			env.print("agent prompt: %s (paste it into agent instructions)", report.PromptPath)
		} else {
			env.print("agent prompt: missing at %s", report.PromptPath)
			env.print("      remedy: run `roca init` to generate it")
		}
	}
	if len(report.CapabilityProposals) > 0 {
		env.print("open capability proposals:")
		for _, proposal := range report.CapabilityProposals {
			env.print("  - %s", proposal)
		}
	}
}

func renderResidentDoctor(env *cliEnv, report *resident.Status) {
	if report == nil {
		env.print("resident: down")
		return
	}
	env.print("resident: pid=%d · uptime=%s · attached clients=%d · open connections=%d",
		report.PID, doctorDuration(report.UptimeMS), report.AttachedClients, report.OpenConnections)
	if len(report.WAL) == 0 {
		env.print("resident WALs: none")
		return
	}
	env.print("resident WALs:")
	paths := make([]string, 0, len(report.WAL))
	for path := range report.WAL {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		env.print("  %s · %d bytes", path, report.WAL[path])
	}
}

func doctorDuration(milliseconds int64) string {
	if milliseconds < 1000 {
		return fmt.Sprintf("%dms", milliseconds)
	}
	return (time.Duration(milliseconds) * time.Millisecond).String()
}

func renderQueryKnobs(query service.QueryDoctor) string {
	templates := "default"
	if !query.ExpandTemplates {
		templates = "false"
	} else if len(query.Templates) > 0 {
		templates = strings.Join(query.Templates, ", ")
	}
	return fmt.Sprintf("query: oversample %d · templates %s · rrf_k %d · min_vector_score %s · max_rare_terms %d · parallel_legs %t",
		query.Oversample, templates, query.RRFK, strconv.FormatFloat(query.MinVectorScore, 'f', -1, 64),
		query.MaxRareTerms, query.ParallelLegs)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func (env *cliEnv) collectForeignOwnedState() []stateOwnership {
	paths, err := env.resolvePaths()
	if err != nil || paths.Home == "" {
		return nil
	}
	repairs := securefile.ScanForeignOwned(filepath.Join(paths.Home, config.DirOwn))
	found := make([]stateOwnership, 0, len(repairs))
	for _, repair := range repairs {
		found = append(found, stateOwnership{
			Path: repair.Path, Owner: repair.Owner, Command: repair.Command,
		})
	}
	return found
}

func renderForeignOwnedState(env *cliEnv, found []stateOwnership) {
	renderForeignOwnedStateTo(env.out, found)
}

func renderForeignOwnedStateTo(out io.Writer, found []stateOwnership) {
	for _, item := range found {
		fmt.Fprintf(out, "state file owned by %s: %s\n", item.Owner, item.Path)
		fmt.Fprintf(out, "      remedy: %s\n", item.Command)
	}
}
