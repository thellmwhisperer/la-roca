package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

const envRocaPrefix = "ROCA_PREFIX"

func pluginCommand(env *cliEnv) *cobra.Command {
	var consented bool
	command := &cobra.Command{
		Use:   "plugin",
		Short: "Install, update, or uninstall an experimental plugin",
		Long: "Manages verified plugin packages from a local directory, a Git URL, or\n" +
			"an owner/repo source. This experimental surface requires features.plugins=true.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.PersistentFlags().BoolVar(&consented, "yes", false, "accept the displayed plugin risk without prompting")
	command.AddCommand(pluginInstallCommand(env, &consented), pluginUpdateCommand(env, &consented),
		pluginUninstallCommand(env, &consented))
	return command
}

func pluginInstallCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "install <path|url|owner/repo>",
		Short: "Verify a source and install its plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, scratch, err := env.pluginManager()
			if err != nil {
				return err
			}
			candidate, cleanup, err := resolvePluginCandidate(cmd.Context(), args[0], scratch)
			if err != nil {
				return err
			}
			defer cleanup()
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "install", candidate, *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Install(candidate)
			if err != nil {
				return err
			}
			return env.reportPlugin("installed", result)
		},
	}
}

func pluginUpdateCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "update <name>",
		Short: "Refresh a plugin from its recorded source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, scratch, err := env.pluginManager()
			if err != nil {
				return err
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(manager.PluginRoot, args[0]))
			if err != nil {
				return err
			}
			candidate, cleanup, err := resolvePluginCandidate(cmd.Context(), manifest.Source, scratch)
			if err != nil {
				return err
			}
			defer cleanup()
			if candidate.Name != args[0] {
				return fmt.Errorf("recorded source now names plugin %q, not %q; update refused",
					candidate.Name, args[0])
			}
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "update", candidate, *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Update(candidate)
			if err != nil {
				return err
			}
			return env.reportPlugin("updated", result)
		},
	}
}

func pluginUninstallCommand(env *cliEnv, consented *bool) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <name>",
		Short: "Remove a plugin, protecting custodial data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, _, err := env.pluginManager()
			if err != nil {
				return err
			}
			manifest, err := plugininstall.ReadManifest(filepath.Join(manager.PluginRoot, args[0]))
			if err != nil {
				return err
			}
			candidate := plugininstall.Candidate{
				Name: manifest.Name, Version: manifest.Version, Source: manifest.Source,
				Checksum: manifest.Checksum, Risk: manifest.Risk, Custody: manifest.Custody,
			}
			accepted, err := env.confirmPlugin(cmd.InOrStdin(), "uninstall", candidate, *consented)
			if err != nil || !accepted {
				return err
			}
			result, err := manager.Uninstall(args[0])
			if err != nil {
				return err
			}
			return env.reportPlugin("uninstalled", result)
		},
	}
}

func (env *cliEnv) pluginManager() (plugininstall.Manager, string, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return plugininstall.Manager{}, "", err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return plugininstall.Manager{}, "", err
	}
	if !file.Features.Plugins {
		return plugininstall.Manager{}, "", fmt.Errorf(
			"the experimental plugin system is disabled; set features.plugins = true in %s",
			paths.Config)
	}
	if paths.Home == "" {
		return plugininstall.Manager{}, "", fmt.Errorf("I do not know where your HOME is; plugin installation requires ~/.roca/plugins")
	}
	bin := os.Getenv(envRocaPrefix)
	if bin == "" {
		bin = filepath.Join(paths.Home, ".local", "bin")
	}
	root := filepath.Join(paths.Home, config.DirOwn)
	return plugininstall.Manager{
		PluginRoot: filepath.Join(root, "plugins"), BinDir: bin,
		ArchiveRoot: filepath.Join(root, "plugin-custody"),
	}, filepath.Join(root, ".plugin-downloads"), nil
}

func resolvePluginCandidate(ctx context.Context, reference, scratch string) (plugininstall.Candidate, func(), error) {
	resolved, cleanup, err := plugininstall.Resolve(ctx, reference, scratch)
	if err != nil {
		return plugininstall.Candidate{}, func() {}, err
	}
	candidate, err := plugininstall.Inspect(resolved.Reference, resolved.Directory)
	if err != nil {
		cleanup()
		return plugininstall.Candidate{}, func() {}, err
	}
	return candidate, cleanup, nil
}

func pluginConsentText(action string, candidate plugininstall.Candidate) string {
	var risk string
	switch candidate.Risk {
	case plugininstall.Executable:
		risk = "EXECUTABLE: FULL TRUST; it runs code with your user privileges."
	default:
		risk = "DATA-ONLY: near-harmless; its worst case is lying content returned from its database."
	}
	custody := ""
	if candidate.Custody {
		custody = "\ncustody: protected; uninstall archives this directory instead of deleting it"
	}
	return fmt.Sprintf("Plugin %s consent\nsource: %s\nversion: %s\nchecksum: sha256:%s\nrisk: %s%s\n",
		action, candidate.Source, candidate.Version, candidate.Checksum, risk, custody)
}

func (env *cliEnv) confirmPlugin(input io.Reader, action string, candidate plugininstall.Candidate,
	consented bool) (bool, error) {

	text := pluginConsentText(action, candidate)
	if consented {
		if !env.json {
			fmt.Fprint(env.errOut, text)
			fmt.Fprintln(env.errOut, "consent: accepted by --yes")
		}
		return true, nil
	}
	if env.json {
		return false, fmt.Errorf("plugin %s needs consent; inspect it without --json or pass --yes", action)
	}
	fmt.Fprint(env.errOut, text)
	fmt.Fprintf(env.errOut, "Proceed with plugin %s? [y/N] ", action)
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("read plugin consent: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	case "", "n", "no":
		if !env.json {
			env.print("plugin %s canceled", action)
		}
		return false, nil
	default:
		return false, fmt.Errorf("plugin consent %q is not valid; answer yes or no", strings.TrimSpace(line))
	}
}

func (env *cliEnv) reportPlugin(action string, result plugininstall.Result) error {
	document := map[string]any{
		"action": action, "name": result.Name, "version": result.Version,
		"checksum": result.Checksum, "risk": result.Risk, "directory": result.Directory,
	}
	if result.Executable != "" {
		document["executable"] = result.Executable
	}
	if result.ArchivedTo != "" {
		document["archived_to"] = result.ArchivedTo
	}
	if env.json {
		return env.printJSON(document)
	}
	if result.ArchivedTo != "" {
		env.print("plugin %s %s; custodial data archived at %s", result.Name, action, result.ArchivedTo)
		return nil
	}
	env.print("plugin %s %s at %s", result.Name, action, result.Directory)
	return nil
}
