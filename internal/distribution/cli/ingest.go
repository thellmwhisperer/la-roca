package cli

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strings"
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
	keyWorkspaceRoots       = "workspace_roots"
	keySubagentRoots        = "subagent_roots"
	keyAnthropicExportPaths = "anthropic_export_paths"
	keyOpenAIExportPaths    = "openai_export_paths"
)

func ingestCommand(env *cliEnv) *cobra.Command {
	var req service.IngestRequest
	var verbose bool
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
			env.capture(result)
			if err != nil {
				return err
			}
			if !env.ingestStarted.IsZero() {
				result.TotalElapsedMS = time.Since(env.ingestStarted).Milliseconds()
			}
			if env.json {
				return env.printJSON(result)
			}
			renderIngest(env, result, verbose)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&req.DryRun, "dry-run", false,
		"report what would be read without writing anything")
	cmd.Flags().BoolVar(&verbose, "verbose", false,
		"add up to 100 record details with paths; the ingest log has the full run report")
	return cmd
}

// renderIngest is the readable output. The same report --json hands over whole.
//
// What the default says is what happened: what each source contributed, and what
// was left out collapsed onto the reason it was left out, with the records this
// build never meant to read named as the exclusions they are. The per-record
// detail retained by the report, and its absolute paths, are behind `--verbose`;
// the same bounded report is in the ingest log either way.
func renderIngest(env *cliEnv, result service.IngestResult, verbose bool) {
	env.print("setup: agents detected: %s · agents not found: %s",
		detectedAgentsLine(result.DetectedAgents), missingAgentsLine(result.DetectedAgents))
	for _, warning := range result.Warnings {
		env.print("  warning: %s", warning)
	}
	if result.DryRun {
		env.print("ingest: dry run · %s pending · %s skipped · %s",
			axi.Quantity(int64(result.FilesRead), "file"), axi.Number(int64(result.FilesSkipped)),
			axi.Duration(result.ElapsedMS))
	} else {
		env.print("ingest: %s read · %s skipped · %s · %s",
			axi.Quantity(int64(result.FilesRead), "file"), axi.Number(int64(result.FilesSkipped)),
			axi.Quantity(int64(result.Errors), "error"), axi.Duration(result.ElapsedMS))
	}
	renderIngestSources(env, result)
	renderIngestDelta(env, result.Delta)
	if result.ExchangesHeld > 0 {
		env.print("  %s still being written and left for the next run",
			axi.Quantity(int64(result.ExchangesHeld), "exchange"))
	}
	renderIngestOutcome(env, result, verbose)
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
		// The discard count earns its place on the row only when there is one:
		// printing "0 discarded" beside every healthy source is what taught an
		// operator to read the row looking for bad news.
		discarded := ""
		if stats.RecordsDiscarded > 0 {
			discarded = fmt.Sprintf("%s discarded · ", axi.Number(int64(stats.RecordsDiscarded)))
		}
		env.print("  ✓ %s · %s · %s · %s · %s · %s%s",
			ingestSourceLabel(name), axi.Quantity(int64(stats.Read), "file"),
			axi.Quantity(int64(sessions), "session"),
			axi.Quantity(int64(counts.Exchanges), "exchange"),
			axi.Quantity(int64(counts.MemoriesInserted+counts.MemoriesUpdated), "memory", "memories"),
			discarded, axi.Duration(stats.ElapsedMS))
	}
}

func renderIngestDelta(env *cliEnv, delta ingest.Tables) {
	env.print("delta: memories=%s sessions=%s exchanges=%s thinking_blocks=%s tool_uses=%s",
		axi.Number(int64(delta.Memories)), axi.Number(int64(delta.Sessions)),
		axi.Number(int64(delta.Exchanges)), axi.Number(int64(delta.ThinkingBlocks)),
		axi.Number(int64(delta.ToolUses)))
}

// categoriesShown bounds the collapsed block. A run over a large corpus meets
// dozens of categories and the tail of them is noise; the counts above the block
// are always the whole truth.
const categoriesShown = 5

var absolutePathInError = regexp.MustCompile(
	`(^|[[:space:]"'(\[])((?:/[^:\n"')]+)|(?:[A-Za-z]:[\\/][^:\n"')]+))(:|["')\]])`,
)

func renderIngestOutcome(env *cliEnv, result service.IngestResult, verbose bool) {
	for _, failure := range result.ErrorDetails {
		path := compactIngestPath(failure.Path)
		if failure.Path == "" {
			path = "unknown file"
		}
		reason := compactIngestError(failure.Reason, failure.Path)
		if verbose {
			reason = failure.Reason
			if failure.Path != "" {
				path = failure.Path
			}
		}
		env.print("error: %s (%s): %s", path, failure.Parser, reason)
	}
	renderIngestCategories(env, result, true,
		fmt.Sprintf("excluded: %s left out by design",
			axi.Quantity(int64(result.RecordsExcluded), "record")))
	renderIngestCategories(env, result, false,
		fmt.Sprintf("discards: %s could not be read",
			axi.Quantity(int64(result.RecordsDiscarded), "record")))
	if !verbose {
		if len(result.DiscardDetails) > 0 {
			env.print("  detail: run `roca ingest --verbose` for up to 100 records and paths; the ingest log has the full run report")
		}
		return
	}
	for _, discard := range result.DiscardDetails {
		label := "discard"
		if discard.ByDesign {
			label = "excluded"
		}
		env.print("%s: %s (%s record %s): %s", label,
			discard.Path, discard.Parser, axi.Number(int64(discard.Record)), discard.Reason)
	}
}

func compactIngestError(reason, knownPath string) string {
	if knownPath != "" {
		reason = strings.ReplaceAll(reason, knownPath, compactIngestPath(knownPath))
	}
	return absolutePathInError.ReplaceAllStringFunc(reason, func(match string) string {
		parts := absolutePathInError.FindStringSubmatch(match)
		return parts[1] + compactIngestPath(parts[2]) + parts[3]
	})
}

func compactIngestPath(path string) string {
	path = strings.TrimRight(path, `/\`)
	if at := strings.LastIndexAny(path, `/\`); at >= 0 {
		return path[at+1:]
	}
	return path
}

// renderIngestCategories prints one side of the outcome collapsed onto its
// reasons, and prints nothing at all when that side is empty.
func renderIngestCategories(env *cliEnv, result service.IngestResult, byDesign bool, headline string) {
	var categories []ingest.DiscardCategory
	for _, category := range result.DiscardSummary {
		if category.ByDesign == byDesign {
			categories = append(categories, category)
		}
	}
	if len(categories) == 0 {
		return
	}
	slices.SortStableFunc(categories, func(a, b ingest.DiscardCategory) int {
		return b.Count - a.Count
	})
	env.print("%s", headline)
	for _, category := range categories[:min(len(categories), categoriesShown)] {
		env.print("  · %s · %s", category.Reason, axi.Number(int64(category.Count)))
	}
	if rest := len(categories) - categoriesShown; rest > 0 {
		env.print("  · %s more", axi.Quantity(int64(rest), "reason"))
	}
}

// ingestSourceLabel is the name one source answers to on every line of a report.
//
// `claude` is the scan's own key for the family the supported roster calls
// `claude-code`, and the label is a property of the source and not of what it
// happened to write: deriving it from the session count renamed the source
// mid-run, so the moving rows said "claude" until the first session landed and
// "claude-code" from then on.
func ingestSourceLabel(name string) string {
	if name == "claude" {
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
func ingestSources(file config.File, home, runnerDir string) ingest.Roots {
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
			RunnerDir:             runnerDir,
			WorkspaceRoots:        file.DefaultList(keyWorkspaceRoots),
			SubagentRoots:         file.DefaultList(keySubagentRoots),
			AnthropicExportPaths:  file.DefaultList(keyAnthropicExportPaths),
			OpenAIExportPaths:     file.DefaultList(keyOpenAIExportPaths),
		})
}
