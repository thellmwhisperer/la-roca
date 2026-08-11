package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// storeCommand is the write half of the product on the shell. It is the same
// service call the plug's `roca_store` makes, with `surface` telling them apart
// afterwards.
func storeCommand(env *cliEnv) *cobra.Command {
	var req service.StoreRequest
	var metadata string
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Write one memory",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, _, err := env.openService()
			if err != nil {
				return err
			}
			defer svc.Close()

			if metadata != "" {
				if err := json.Unmarshal([]byte(metadata), &req.Metadata); err != nil {
					return fmt.Errorf("--metadata is not a JSON object: %w", err)
				}
				// `null` decodes without error and leaves no map behind, so the
				// flag was accepted while carrying nothing the message promised.
				if req.Metadata == nil {
					return fmt.Errorf("--metadata is not a JSON object: it is null")
				}
			}
			req.Surface = service.SurfaceCLI
			result, err := svc.Store(cmd.Context(), req)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(result)
			}
			env.print("%s", axi.Store(result))
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Layer, "layer", "", "the layer the memory belongs to")
	cmd.Flags().StringVar(&req.Content, "content", "", "the content of the memory")
	cmd.Flags().StringVar(&req.Origin, "origin", "", "who creates it: human, agent, cron or plugin:<name>")
	cmd.Flags().StringVar(&req.SourceAgent, "source-agent", "", "which agent is writing it")
	cmd.Flags().StringVar(&req.Project, "project", "", "project scope (omit for global)")
	cmd.Flags().StringVar(&req.Status, "status", "", "active, pending or resolved")
	cmd.Flags().Int64Var(&req.Supersedes, "supersedes", 0, "id of the memory this one replaces")
	cmd.Flags().StringVar(&metadata, "metadata", "", "structured tags, as a JSON object")
	cmd.MarkFlagRequired("layer")
	cmd.MarkFlagRequired("content")
	return cmd
}

// healthCommand reads live data and says what is broken in it. It writes
// nothing, which is why it still answers in read-only mode.
func healthCommand(env *cliEnv) *cobra.Command {
	var req service.HealthRequest
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Non-destructive checks over live data",
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			report, err := svc.Health(cmd.Context(), req)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(report)
			}
			env.print("%s", axi.Health(report))
			return nil
		}),
	}
	cmd.Flags().IntVar(&req.MaxRows, "max-rows", 0, "sample rows per check")
	return cmd
}
