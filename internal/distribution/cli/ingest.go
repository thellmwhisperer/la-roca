package cli

import (
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// These keys are read under [defaults] and at the document root, so a hand-written
// config is not invisible.
const (
	keyWorkspaceRoots = "workspace_roots"
	keySubagentRoots  = "subagent_roots"
)

func ingestCommand(env *cliEnv) *cobra.Command {
	var req service.IngestRequest
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "Read every source of the matrix and normalize what changed",
		Long: "Reads the artefact families of the agents detected on this machine, normalizes\n" +
			"them into the database and refreshes the search index.\n\n" +
			"It is incremental: a file whose fingerprint has not changed is not even\n" +
			"opened, so running it repeatedly is cheap and produces the same state.\n" +
			"`--dry-run` reports what it would read and writes nothing.",
		PreRun: func(*cobra.Command, []string) {
			env.wantIngestProgress = true
			env.ingestStarted = time.Now()
		},
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			result, err := svc.Ingest(cmd.Context(), req)
			env.finishIngestProgress()
			if err != nil {
				return err
			}
			if !env.ingestStarted.IsZero() {
				result.TotalElapsedMS = time.Since(env.ingestStarted).Milliseconds()
			}
			env.capture(result)
			if env.json {
				return env.printJSON(result)
			}
			renderIngest(env, result)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&req.DryRun, "dry-run", false,
		"report what would be read without writing anything")
	return cmd
}

// renderIngest is the readable output. The same report --json hands over whole.
func renderIngest(env *cliEnv, result service.IngestResult) {
	env.print("setup: agents detected: %s · agents not found: %s",
		detectedAgentsLine(result.DetectedAgents), missingAgentsLine(result.DetectedAgents))
	for _, warning := range result.Warnings {
		env.print("  warning: %s", warning)
	}
	if result.DryRun {
		env.print("ingest: dry run · %s files pending · %s skipped · %s",
			axi.Number(int64(result.FilesRead)), axi.Number(int64(result.FilesSkipped)),
			axi.Duration(result.ElapsedMS))
	} else {
		env.print("ingest: %s files read · %s skipped · %s errors · %s",
			axi.Number(int64(result.FilesRead)), axi.Number(int64(result.FilesSkipped)),
			axi.Number(int64(result.Errors)), axi.Duration(result.ElapsedMS))
	}
	renderIngestSources(env, result)
	renderIngestDelta(env, result.Delta)
	if result.ExchangesHeld > 0 {
		env.print("  %s exchanges are still being written and were left for the next run",
			axi.Number(int64(result.ExchangesHeld)))
	}
	renderIngestDetails(env, result)
	if result.Index != nil {
		env.print("index: full-text index ready · %s", axi.Duration(result.Index.ElapsedMS))
	}
	env.print("total: %s", axi.Duration(result.TotalElapsedMS))
	if result.DryRun {
		env.print("next: run `roca ingest` to write the pending files")
	} else {
		env.print("next: run `roca query \"<natural question>\"`")
	}
}

func renderIngestSources(env *cliEnv, result service.IngestResult) {
	for _, name := range ingest.SortedSources(result.Sources) {
		counts := result.Sources[name]
		stats := result.SourceStats[name]
		if stats == nil {
			stats = &ingest.SourceStats{}
		}
		sessions := counts.Sessions + counts.SessionsUpdated
		env.print("  ✓ %s · %s files · %s sessions · %s exchanges · %s memories · %s discarded · %s",
			ingestSourceLabel(name, sessions), axi.Number(int64(stats.Read)),
			axi.Number(int64(sessions)),
			axi.Number(int64(counts.Exchanges)),
			axi.Number(int64(counts.MemoriesInserted+counts.MemoriesUpdated)),
			axi.Number(int64(stats.RecordsDiscarded)), axi.Duration(stats.ElapsedMS))
	}
}

func renderIngestDelta(env *cliEnv, delta ingest.Tables) {
	env.print("delta: memories=%s sessions=%s exchanges=%s thinking_blocks=%s tool_uses=%s",
		axi.Number(int64(delta.Memories)), axi.Number(int64(delta.Sessions)),
		axi.Number(int64(delta.Exchanges)), axi.Number(int64(delta.ThinkingBlocks)),
		axi.Number(int64(delta.ToolUses)))
}

func renderIngestDetails(env *cliEnv, result service.IngestResult) {
	for _, failure := range result.ErrorDetails {
		env.print("error: %s (%s): %s", failure.Path, failure.Parser, failure.Reason)
	}
	for _, discard := range result.DiscardDetails {
		env.print("discard: %s (%s record %s): %s",
			discard.Path, discard.Parser, axi.Number(int64(discard.Record)), discard.Reason)
	}
}

func ingestSourceLabel(name string, sessions int) string {
	if name == "claude" && sessions > 0 {
		return "claude-code"
	}
	return name
}

// ingestSources resolves where every agent's artefacts live on this machine.
//
// The roots are configuration, never constants: they come from the home this
// process was given, from the environment and from what the operator declared,
// and the operator's declaration wins. A path with a machine name inside the
// binary is a guard failure, not a style decision.
func ingestSources(file config.File, home string) ingest.Roots {
	return ingest.ResolveRoots(
		ingest.Environment{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv},
		ingest.Settings{
			ClaudeProjects:        file.Default("claude_projects_root"),
			ClaudeDesktopSessions: file.Default("claude_desktop_sessions_root"),
			CoworkSessions:        file.Default("cowork_sessions_root"),
			CodexRoot:             file.Default("codex_root"),
			CodexSessions:         file.Default("codex_sessions_root"),
			CodexStateDB:          file.Default("codex_state_db_path"),
			OpenCodeDB:            file.Default("opencode_db_path"),
			PiSessions:            file.Default("pi_sessions_root"),
			HermesDB:              file.Default("hermes_db_path"),
			WorkspaceRoots:        file.DefaultList(keyWorkspaceRoots),
			SubagentRoots:         file.DefaultList(keySubagentRoots),
		})
}
