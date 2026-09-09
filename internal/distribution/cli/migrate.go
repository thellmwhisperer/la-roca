package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/datasplit"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func migrateCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use: "migrate", Short: "Resume and verify DATA SPLIT custody migration",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if env.forceReadOnly || config.ReadOnly(os.Getenv(config.EnvReadOnly)) {
				return fmt.Errorf("roca migrate requires writable databases")
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			root := pluginRoot(paths)
			for _, ensure := range []func(string, string, string) (plugininstall.Result, error){
				rocaops.Ensure, rocacorpus.Ensure, rocacron.Ensure,
			} {
				if _, err := ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
					return err
				}
			}
			report, err := datasplit.Migrate(cmd.Context(), migrationHubOptions(paths))
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{"verified": report.Ready})
			}
			env.print("migration: verified")
			return nil
		},
	}
}

func migrationHubOptions(paths config.Paths) datasplit.HubOptions {
	root := pluginRoot(paths)
	return datasplit.HubOptions{
		CoreDatabase:   paths.DB,
		OpsDatabase:    filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename),
		CorpusDatabase: filepath.Join(root, rocacorpus.Name, rocacorpus.DatabaseFilename),
		CronDatabase:   filepath.Join(root, rocacron.Name, rocacron.DatabaseFilename),
		SnapshotDir:    filepath.Join(paths.Backups, "data-split"),
		LockPath:       logfile.New(filepath.Dir(paths.DB)).LockPath(),
	}
}
