package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const claudeHandoffHeadChars = 3000

func pillCommand(env *cliEnv) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "pill",
		Short: "Load active pills for the current project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runPillList(cmd.Context(), env, project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project scope (default: basename of the working directory)")
	cmd.AddCommand(pillShowCommand(env, &project))
	cmd.AddCommand(pillDeleteCommand(env))
	return cmd
}

func pillShowCommand(env *cliEnv, project *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <slug>",
		Short: "Load one complete pill by slug",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveProject(*project)
			if err != nil {
				return err
			}
			svc, _, err := env.openSessionContextService()
			if err != nil {
				return err
			}
			defer svc.Close()
			record, err := svc.ShowPill(cmd.Context(), resolved, args[0])
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(record)
			}
			env.print("%s", axi.Pill(record))
			return nil
		},
	}
	cmd.Flags().StringVar(project, "project", "", "project scope (default: basename of the working directory)")
	return cmd
}

func pillDeleteCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <slug>",
		Short: "Delete every stored version of a pill slug",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, _, err := env.openPillDeleteService()
			if err != nil {
				return err
			}
			defer svc.Close()
			result, err := svc.DeletePill(cmd.Context(), args[0])
			if err != nil {
				if len(result.Known) > 0 {
					env.print("%s\n", axi.RenderHelp(result.Known...))
				}
				return err
			}
			env.print("deleted: %d\n", result.Deleted)
			return nil
		},
	}
}

func handoffCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Load session-continuity handoffs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(handoffLatestCommand(env))
	return cmd
}

func handoffLatestCommand(env *cliEnv) *cobra.Command {
	var project string
	var limit int
	var allProjects bool
	var since string
	cmd := &cobra.Command{
		Use:   "latest",
		Short: "Load active handoffs the project has not superseded",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLatestHandoffs(cmd.Context(), env, latestHandoffOptions{
				project: project, limit: limit, allProjects: allProjects, since: since,
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project scope (default: basename of the working directory)")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum handoffs to print (default: unlimited)")
	cmd.Flags().BoolVar(&allProjects, "all-projects", false, "print the newest handoff head for every project")
	cmd.Flags().StringVar(&since, "since", "", "only include --all-projects handoffs newer than this nonnegative day count, e.g. 30d")
	return cmd
}

func runPillList(ctx context.Context, env *cliEnv, project string) error {
	return runSessionContext(ctx, env, project, (*service.Service).ListPills, axi.Pills)
}

type latestHandoffOptions struct {
	project     string
	limit       int
	allProjects bool
	since       string
	headChars   int
}

func runLatestHandoffs(ctx context.Context, env *cliEnv, opts latestHandoffOptions) error {
	if opts.limit < 0 {
		return fmt.Errorf("--limit must be zero or greater")
	}
	if opts.allProjects {
		return runLatestHandoffsAllProjects(ctx, env, opts)
	}
	return runSessionContext(ctx, env, opts.project,
		func(svc *service.Service, ctx context.Context, project string) (service.HandoffList, error) {
			list, err := svc.LatestHandoffs(ctx, project)
			if err != nil {
				return service.HandoffList{}, err
			}
			list.Handoffs = limitSlice(list.Handoffs, opts.limit)
			return list, nil
		},
		func(list service.HandoffList) string {
			if opts.headChars > 0 {
				return axi.HandoffHeads(list, opts.headChars)
			}
			return axi.Handoffs(list)
		})
}

func runSessionContext[T any](ctx context.Context, env *cliEnv, project string,
	load func(*service.Service, context.Context, string) (T, error), render func(T) string) error {
	resolved, err := resolveProject(project)
	if err != nil {
		return err
	}
	svc, _, err := env.openSessionContextService()
	if err != nil {
		return err
	}
	defer svc.Close()
	result, err := load(svc, ctx, resolved)
	if err != nil {
		return err
	}
	if env.json {
		return env.printJSON(result)
	}
	env.print("%s", render(result))
	return nil
}

func runLatestHandoffsAllProjects(ctx context.Context, env *cliEnv, opts latestHandoffOptions) error {
	if opts.project != "" {
		return fmt.Errorf("--project cannot be combined with --all-projects")
	}
	since, err := parseSince(opts.since)
	if err != nil {
		return err
	}
	headChars := opts.headChars
	if headChars == 0 {
		headChars = claudeHandoffHeadChars
	}
	svc, _, err := env.openSessionContextService()
	if err != nil {
		return err
	}
	defer svc.Close()
	result, err := svc.LatestHandoffsByProject(ctx, since, headChars)
	if err != nil {
		return err
	}
	result.Rows = limitSlice(result.Rows, opts.limit)
	if env.json {
		return env.printJSON(result)
	}
	env.print("%s", axi.HandoffLab(result))
	return nil
}

func limitSlice[T any](items []T, limit int) []T {
	if limit <= 0 || len(items) <= limit {
		return items
	}
	return items[:limit]
}

func parseSince(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	days, err := strconv.ParseUint(strings.TrimSuffix(value, "d"), 10, 64)
	if !strings.HasSuffix(value, "d") || err != nil || days > uint64((1<<63-1)/(24*time.Hour)) {
		return time.Time{}, fmt.Errorf("--since must be a nonnegative day count like 30d")
	}
	return time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour), nil
}

func (env *cliEnv) openSessionContextService() (*service.Service, config.Paths, error) {
	return env.openSessionContextServiceReadOnly(true)
}

func (env *cliEnv) openPillDeleteService() (*service.Service, config.Paths, error) {
	return env.openSessionContextServiceReadOnly(false)
}

func (env *cliEnv) openSessionContextServiceReadOnly(readOnly bool) (*service.Service, config.Paths, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, paths, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return nil, paths, err
	}
	if !file.Features.RocaOps {
		return nil, paths, fmt.Errorf("session context requires features.roca_ops and an existing roca-ops database")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil, paths, fmt.Errorf("session context requires a HOME containing the roca-ops database")
	}
	opsDatabase := filepath.Join(home, config.DirOwn, "plugins", rocaops.Name, rocaops.DatabaseFilename)
	if !fileExists(opsDatabase) {
		return nil, paths, fmt.Errorf("session context requires the existing roca-ops database at %s", opsDatabase)
	}
	scoped := *env
	scoped.omitCorpus = true
	if readOnly {
		scoped.forceReadOnly = true
	}
	return scoped.openService()
}

func resolveProject(project string) (string, error) {
	if project != "" {
		return project, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve the working directory: %w", err)
	}
	base := filepath.Base(cwd)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "", fmt.Errorf("a --project is required when the working directory has no basename")
	}
	return base, nil
}
