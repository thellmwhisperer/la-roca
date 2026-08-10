package cli

import (
	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
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
			env.print("%s", axi.Duration(report.ElapsedMS))
			return nil
		}),
	}
}
