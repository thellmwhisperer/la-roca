package cli

import (
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/human"
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
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			result, err := svc.Ingest(cmd.Context(), req)
			if err != nil {
				return err
			}
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
	for _, warning := range result.Warnings {
		env.print("warning: %s", warning)
	}
	if result.DryRun {
		env.print("dry run: nothing was written")
	}
	env.print("%d files pending, %d skipped by fingerprint", result.FilesRead, result.FilesSkipped)
	for _, name := range ingest.SortedSources(result.Sources) {
		counts := result.Sources[name]
		env.print("  %s: %d sessions · %d exchanges · %d memories",
			name, counts.Sessions,
			counts.Exchanges, counts.MemoriesInserted+counts.MemoriesUpdated)
	}
	env.print("delta: memories=%d sessions=%d exchanges=%d thinking_blocks=%d tool_uses=%d",
		result.Delta.Memories, result.Delta.Sessions, result.Delta.Exchanges,
		result.Delta.ThinkingBlocks, result.Delta.ToolUses)
	if result.ExchangesHeld > 0 {
		env.print("%d exchanges are still being written and were left for the next run",
			result.ExchangesHeld)
	}
	for _, failure := range result.ErrorDetails {
		env.print("error: %s (%s): %s", failure.Path, failure.Kind, failure.Reason)
	}
	if result.Index != nil {
		if result.Index.LexicalBuilt {
			env.print("index: full-text index built")
		}
	}
	env.print("%d errors · %s", result.Errors, human.Duration(result.ElapsedMS))
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
