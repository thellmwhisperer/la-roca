package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
)

func pillCommand(env *cliEnv) *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "pill",
		Short: "Load active pills for the current project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveProject(project)
			if err != nil {
				return err
			}
			svc, _, err := env.openStoreService()
			if err != nil {
				return err
			}
			defer svc.Close()
			list, err := svc.ListPills(cmd.Context(), resolved)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(list)
			}
			env.print("%s", axi.Pills(list))
			return nil
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
			svc, _, err := env.openStoreService()
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
			resolved, err := resolveProject(project)
			if err != nil {
				return err
			}
			svc, _, err := env.openStoreService()
			if err != nil {
				return err
			}
			defer svc.Close()
			list, err := svc.LatestHandoffs(cmd.Context(), resolved)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(list)
			}
			env.print("%s", axi.Handoffs(list))
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project scope (default: basename of the working directory)")
	return cmd
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
