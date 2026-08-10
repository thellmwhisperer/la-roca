package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/bench"
	"github.com/thellmwhisperer/la-roca/internal/human"
	"github.com/thellmwhisperer/la-roca/internal/service"
)

func indexCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the search index",
		Long: "Builds the full-text index.\n" +
			"It is incremental: on an already indexed database it costs nothing, and\n" +
			"`roca init` calls it on its own. Run it by hand to pick up memory that\n" +
			"another process wrote.",
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			report, err := svc.Index(cmd.Context())
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(report)
			}
			if report.LexicalBuilt {
				env.print("full-text index built")
			}
			env.print("%s", human.Duration(report.ElapsedMS))
			return nil
		}),
	}
}

func benchCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Measure La Roca against a golden query bench",
	}
	cmd.AddCommand(benchGoldenCommand(env))
	return cmd
}

func benchGoldenCommand(env *cliEnv) *cobra.Command {
	var methods []string
	cmd := &cobra.Command{
		Use:   "golden <file>",
		Short: "Run a golden query bench and publish the score per search method",
		Long: "Runs every query in the bench through every search method and reports\n" +
			"how many passed and how fast. The bench is your own data file, generated\n" +
			"from your own corpus: this binary ships no questions inside it. The file\n" +
			"format is documented in docs/golden-bench.md.",
		Args: cobra.ExactArgs(1),
		RunE: env.serviceRunE(func(cmd *cobra.Command, args []string, svc *service.Service) error {
			goldenBench, err := bench.Load(args[0])
			if err != nil {
				return err
			}
			result, err := bench.Run(cmd.Context(), goldenBench, args[0],
				querier{svc: svc}, methods)
			if err != nil {
				return err
			}
			for _, v := range result.Verdicts {
				if !v.Passed {
					env.code = ExitRefused
					break
				}
			}
			if env.json {
				return env.printJSON(result)
			}
			bench.Write(env.out, result)
			return nil
		}),
	}
	cmd.Flags().StringSliceVar(&methods, "method", nil,
		"search methods to compare (default: "+strings.Join(bench.Competitors, ", ")+")")
	return cmd
}

// querier adapts the service to what the bench needs. It is a pure adapter, with
// no logic: any logic here would be logic the MCP plug cannot reach.
type querier struct{ svc *service.Service }

func (c querier) Ask(ctx context.Context, question, method string) (bench.Observed, error) {
	res, err := c.svc.Search(ctx, service.SearchRequest{
		Question: question,
		Method:   method,
	})
	if err != nil {
		return bench.Observed{}, err
	}
	return res.Observed(), nil
}
