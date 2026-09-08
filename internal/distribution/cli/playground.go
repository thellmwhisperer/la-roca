package cli

import (
	"context"
	"errors"
	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/playground"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"io"
	"os/exec"
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
			if _, err := playground.Executable(); err != nil && (slices.Contains(args, "--help") || slices.Contains(args, "-h")) {
				cmd.Printf("%s\nplayground for humans: --full interprets rows; --sql-only generates SQL; explore --deep investigates a concept\n", playground.InstallHint)
				return nil
			}
			// Parse core's inherited flags for path resolution and audit policy while
			// forwarding the original argv, including the plugin's own flags.
			cmd.FParseErrWhitelist.UnknownFlags = true
			cmd.DisableFlagParsing = false
			parseErr := cmd.ParseFlags(args)
			cmd.DisableFlagParsing = true
			if parseErr != nil {
				return parseErr
			}
			if _, err := playground.Executable(); err != nil {
				return err
			}
			if (verb == "playground" || verb == "explore") && !slices.Contains(args, "--help") && !slices.Contains(args, "-h") {
				svc, _, err := env.openService()
				if err != nil {
					return err
				}
				if err := svc.Close(); err != nil {
					return err
				}
			}
			forwarded := []string{verb}
			if env.dbPath != "" {
				forwarded = append(forwarded, "--db-path", env.dbPath)
			}
			if env.json {
				forwarded = append(forwarded, "--json")
			}
			if env.forceReadOnly {
				forwarded = append(forwarded, "--read-only")
			}
			forwarded = append(forwarded, args...)
			audit, err := playground.Run(cmd.Context(), forwarded, cmd.InOrStdin(), env.out, env.errOut)
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
