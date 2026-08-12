package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/reconcile"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func (env *cliEnv) reconciliationContext() (reconcile.Context, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return reconcile.Context{}, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return reconcile.Context{}, err
	}
	backups, err := recoveryBackups(paths.Config)
	if err != nil {
		return reconcile.Context{}, err
	}
	return reconcile.Context{
		Version: env.build.Version, ConfigPath: paths.Config,
		StampPath: paths.Reconciliation,
		LookPath:  exec.LookPath, Env: os.Getenv, File: file,
		RetiredCredentialPaths: legacyProviderCredentialPaths(dirOf(paths.DB)),
		RecoveryBackupPaths:    backups,
		Capabilities:           map[string]bool{reconcile.CapabilityAnthropicExport: true},
	}, nil
}

func (env *cliEnv) reconcileAfterCommand(cmd *cobra.Command) error {
	if cmd == nil || env.skipReconciliation {
		return nil
	}
	switch cmd.Name() {
	case "init", "doctor", "update", "uninstall", "_capabilities":
		return nil
	}
	interactive := terminalInput(cmd.InOrStdin()) && !env.json
	_, err := env.reconcileCapabilities(cmd, interactive, false)
	return err
}

func (env *cliEnv) openCapabilityProposals() ([]reconcile.Entry, error) {
	context, err := env.reconciliationContext()
	if err != nil {
		return nil, err
	}
	return reconcile.Open(context, reconcile.Registry()), nil
}

func (env *cliEnv) reconcileCapabilities(cmd *cobra.Command, interactive, listAll bool) (reconcile.Result, error) {
	context, err := env.reconciliationContext()
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Run(context, reconcile.Registry(), reconcile.Options{
		Interactive: interactive, ListAll: listAll,
		In: cmd.InOrStdin(), Out: env.errOut,
	})
}

func capabilityCountLine(count int) string {
	if count == 1 {
		return "1 new capability needs a look: run `roca doctor`"
	}
	return fmt.Sprintf("%d new capabilities need a look: run `roca doctor`", count)
}

func capabilitiesCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:    "_capabilities",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			open, err := env.openCapabilityProposals()
			if err != nil {
				return err
			}
			return env.printJSON(map[string]any{"pending": len(open)})
		},
	}
}
