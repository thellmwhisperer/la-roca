package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

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
	cmd.PersistentFlags().StringVar(&project, "project", "", "project scope (default: basename of the working directory)")
	cmd.AddCommand(pillShowCommand(env, &project))
	return cmd
}

func pillShowCommand(env *cliEnv, project *string) *cobra.Command {
	return &cobra.Command{
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
	cmd := &cobra.Command{
		Use:   "latest",
		Short: "Load active handoffs the project has not superseded",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLatestHandoffs(cmd.Context(), env, project)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project scope (default: basename of the working directory)")
	return cmd
}

func runPillList(ctx context.Context, env *cliEnv, project string) error {
	return runSessionContext(ctx, env, project, (*service.Service).ListPills, axi.Pills)
}

func runLatestHandoffs(ctx context.Context, env *cliEnv, project string) error {
	return runSessionContext(ctx, env, project, (*service.Service).LatestHandoffs, axi.Handoffs)
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

func (env *cliEnv) openSessionContextService() (*service.Service, config.Paths, error) {
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
	scoped.forceReadOnly = true
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
