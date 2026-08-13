package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

func installBundledPluginsCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:    "_install-bundled-plugins",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			result, err := rocaops.Ensure(pluginRoot(paths), pluginExecutableDir(paths), env.build.Version)
			if err != nil {
				return err
			}
			return env.report(map[string]any{
				"installed": true, "plugin": result.Name, "version": result.Version,
				"risk": result.Risk, "resident": true,
			}, "bundled plugin %s %s at %s", result.Name, result.Version, result.Directory)
		},
	}
}

func opsCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "ops",
		Short: "Operate the experimental resident operational store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(opsDrainCommand(env))
	return command
}

func opsDrainCommand(env *cliEnv) *cobra.Command {
	var before string
	command := &cobra.Command{
		Use:   "drain",
		Short: "Remove operational rows whose explicit expiry is due",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cutoff := time.Now().UTC()
			if before != "" {
				var err error
				cutoff, err = time.Parse(time.RFC3339, before)
				if err != nil {
					return fmt.Errorf("--before must be RFC3339: %w", err)
				}
			}
			svc, _, err := env.openService()
			if err != nil {
				return err
			}
			defer svc.Close()
			result, err := svc.DrainRocaOps(cmd.Context(), cutoff)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(result)
			}
			env.print("roca-ops drain: %d expired memories removed before %s",
				result.Removed, result.Before)
			return nil
		},
	}
	command.Flags().StringVar(&before, "before", "", "RFC3339 cutoff (default: now)")
	return command
}
