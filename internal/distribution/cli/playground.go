package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/datasplit"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/playground"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// OpenForPlugin resolves the same installation and read-only engine as core.
func OpenForPlugin(build Build, dbPath string, readOnly bool, out, errOut io.Writer) (*service.Service, config.Paths, error) {
	env := &cliEnv{build: build, dbPath: dbPath, forceReadOnly: readOnly, skipBundledLifecycle: true, out: out, errOut: errOut}
	env.loadCommandFeatures()
	return env.openService()
}

func playgroundPluginCommand(env *cliEnv, verb string) *cobra.Command {
	return &cobra.Command{Use: verb + " [arguments]", Short: map[string]string{"playground": "Optional plugin: compile a question into SQL", "explore": "Optional plugin: investigate a concept", "model": "Optional plugin: select the answering model", "models": "Optional plugin: list answering models", "login": "Optional plugin: use an agent CLI login"}[verb],
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Cobra preserves the original inherited argv for a command with
			// parsing disabled. Parse that flag set once for core audit policy;
			// forwarding args itself preserves globals, plugin flags and --.
			cmd.Flags().AddFlagSet(cmd.InheritedFlags())
			cmd.Flags().ParseErrorsWhitelist.UnknownFlags = true
			if err := cmd.Flags().Parse(args); err != nil {
				return err
			}
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			path, found := resolveCompanion("playground", filepath.Join(paths.Home, config.DirOwn, "plugins", "roca-playground"))
			if !found {
				if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
					cmd.Printf("%s\nplayground for humans: --full interprets rows; --sql-only generates SQL; explore --deep investigates a concept\n", playground.InstallHint)
					return nil
				}
				return fmt.Errorf("%s", playground.InstallHint)
			}
			if (verb == "playground" || verb == "explore") && !slices.Contains(args, "--help") && !slices.Contains(args, "-h") {
				// Only the installed core version may place bundled packages.
				// The companion opens the federation once, through OpenForPlugin.
				if err := env.preparePlayground(paths); err != nil {
					return err
				}
			}
			audit, err := playground.RunExecutable(cmd.Context(), path, append([]string{verb}, args...), cmd.InOrStdin(), env.out, env.errOut)
			env.auditQuery = audit
			if audit != nil {
				env.capture(*audit)
			}
			var exited *exec.ExitError
			if errors.As(err, &exited) {
				env.code = exited.ExitCode()
				return nil
			}
			return err
		}}
}

func (env *cliEnv) preparePlayground(paths config.Paths) error {
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	if !fileExists(paths.DB) && file.Layout.Serving != config.LayoutCutover {
		return logfile.Typed(fmt.Errorf("no Roca database exists at %s; run `roca init` before this command", paths.DB), logfile.ErrorNotInitialized)
	}
	if env.forceReadOnly || config.ReadOnly(os.Getenv(config.EnvReadOnly)) {
		return nil
	}
	root := filepath.Join(paths.Home, config.DirOwn, "plugins")
	if _, err := rocaops.Ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
		return err
	}
	if _, err := rocacorpus.Ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
		return err
	}
	if err := env.refreshVectorRegistry(); err != nil {
		env.warnVectorRegistryRefresh(err)
	}
	if file.Layout.Serving == config.LayoutLegacyServing || !fileExists(paths.DB) {
		return nil
	}
	if _, err := rocacron.Ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
		return fmt.Errorf("install bundled cron plugin for DATA SPLIT: %w", err)
	}
	_, prepareErr := datasplit.PrepareHub(context.Background(), datasplit.HubOptions{
		CoreDatabase: paths.DB, OpsDatabase: filepath.Join(root, rocaops.Name, rocaops.DatabaseFilename),
		CorpusDatabase: filepath.Join(root, rocacorpus.Name, rocacorpus.DatabaseFilename),
		CronDatabase:   filepath.Join(root, rocacron.Name, rocacron.DatabaseFilename),
		SnapshotDir:    filepath.Join(paths.Backups, "data-split"),
		LockPath:       logfile.New(filepath.Dir(paths.DB)).LockPath(),
	})
	if prepareErr != nil {
		if rollbackErr := config.SetServingLayout(paths.Config, config.LayoutLegacyServing); rollbackErr != nil {
			return errors.Join(prepareErr,
				fmt.Errorf("roll back the DATA SPLIT serving marker: %w", rollbackErr))
		}
		return fmt.Errorf("prepare the federation hub; serving marker returned to legacy-serving: %w",
			prepareErr)
	}
	return nil
}

func providerProbe(paths config.Paths, readOnly bool) func(context.Context, *service.DoctorReport) error {
	if _, err := playground.Executable(); err != nil {
		return nil
	}
	return func(ctx context.Context, report *service.DoctorReport) error {
		var probe service.DoctorReport
		args := []string{"probe", "--db-path", paths.DB}
		if readOnly {
			args = append(args, "--read-only")
		}
		if err := playground.JSON(ctx, args, &probe); err != nil {
			report.Warnings = append(report.Warnings, "playground provider probe: "+err.Error())
			return nil
		}
		report.ProviderNarration = probe.ProviderNarration
		report.Providers, report.Titular = probe.Providers, probe.Titular
		report.Interpreters, report.InterpretTitular = probe.Interpreters, probe.InterpretTitular
		report.Explorers, report.ExploreTitular = probe.Explorers, probe.ExploreTitular
		report.ModelDisabled, report.FactoryDefault = probe.ModelDisabled, probe.FactoryDefault
		report.FactoryDefaultProvider = probe.FactoryDefaultProvider
		report.DetectedModelBinaries, report.MissingModelBinaries = probe.DetectedModelBinaries, probe.MissingModelBinaries
		report.Warnings = append(report.Warnings, probe.Warnings...)
		return nil
	}
}
