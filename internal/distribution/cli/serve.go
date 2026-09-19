package cli

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/mcpplug"
	residentserver "github.com/thellmwhisperer/la-roca/internal/distribution/resident"
	residenttransport "github.com/thellmwhisperer/la-roca/pkg/resident"
)

// serveCommand is the first-class entry point for serving MCP.
//
// It is on demand and in the foreground: the agent launches it, it answers over
// its standard input and output, and it dies when that pipe closes. There is no
// daemon, no port and no supervisor, and this command is the whole of the
// lifecycle those would have needed.
func serveCommand(env *cliEnv) *cobra.Command {
	var residentMode bool
	command := &cobra.Command{
		Use:   "serve",
		Short: "Serve the MCP over stdio, on demand and in the foreground",
		Long: "Starts the MCP server on standard input and output. It is what the\n" +
			"entry written by `roca mcp install` runs in an agent, and\n" +
			"it exits when the agent closes the pipe.\n\n" +
			"Nothing is written to standard output that is not the protocol: a print\n" +
			"there would corrupt the session.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if residentMode {
				svc, _, err := env.openService()
				if err != nil {
					return err
				}
				defer svc.Close()
				return residentserver.Run(cmd.Context(), svc, mcpplug.Build{
					Version: env.build.Version,
					Commit:  env.build.Commit,
				})
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			binary, err := os.Executable()
			if err != nil {
				return err
			}
			return residenttransport.ProxyStdio(cmd.Context(), residenttransport.Options{
				Binary: binary, DataDir: filepath.Dir(paths.DB), DBPath: paths.DB,
			}, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
	command.Flags().BoolVar(&residentMode, "resident", false, "run the shared database resident")
	return command
}
