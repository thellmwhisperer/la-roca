package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/evaluation"
)

func evalCommand(env *cliEnv) *cobra.Command {
	var mode, format, workDir string
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Measure retrieval against the synthetic golden set",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "replay" && mode != "live" {
				return fmt.Errorf("eval mode %q is not replay or live", mode)
			}
			if format != "human" && format != "markdown" && format != "json" {
				return fmt.Errorf("eval format %q is not human, markdown or json", format)
			}
			suite, err := evaluation.LoadSuite()
			if err != nil {
				return err
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			dbPath, cleanup, err := evaluation.PrepareFixture(cmd.Context(), workDir)
			if err != nil {
				return err
			}
			defer cleanup()
			paths.DB = dbPath
			paths.Backups = filepath.Join(filepath.Dir(dbPath), "backups")
			svc, err := env.openServiceWith(paths)
			if err != nil {
				return err
			}
			defer svc.Close()

			planner := evaluation.ReplayPlanner(suite)
			if mode == "live" {
				planner = evaluation.LivePlanner(svc)
			}
			report, err := evaluation.Run(cmd.Context(), svc, suite, planner, mode)
			if err != nil {
				return err
			}
			env.capture(report)
			// Evaluation is a development measurement over a disposable database,
			// not an operator query. Do not add it to the operator's execution log.
			env.prelogged = true
			if env.json || format == "json" {
				return env.printJSON(report)
			}
			if format == "markdown" {
				env.print("%s", evaluation.RenderMarkdown(report))
				return nil
			}
			env.print("%s", evaluation.RenderHuman(report))
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "replay", "plan source: replay or live")
	cmd.Flags().StringVar(&format, "format", "human", "report format: human, markdown or json")
	cmd.Flags().StringVar(&workDir, "work-dir", filepath.Join(".tmp", "eval"),
		"directory for disposable fixture databases")
	return cmd
}
