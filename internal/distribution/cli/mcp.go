package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/artifact"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
)

// EnvExecutable is an explicit test and operator override. Without it, the
// declaration names the absolute path of this process so PATH cannot select a
// different product that happens to be called `roca`.
const EnvExecutable = "ROCA_BIN"

// mcpCommand declares La Roca in the agents' own configuration files, and
// withdraws it again. It does not open the database: an operator wiring up
// their agents has not necessarily run `roca init` yet, and asking them to
// would be a step nobody's flow has.
func mcpCommand(env *cliEnv) *cobra.Command {
	mcp := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP configuration and serve the MCP over stdio",
	}
	mcp.AddCommand(
		mcpInstallCommand(env), mcpUninstallCommand(env), mcpStatusCommand(env), serveCommand(env),
	)
	return mcp
}

func mcpInstallCommand(env *cliEnv) *cobra.Command {
	var configPath, executable string
	cmd := &cobra.Command{
		Use:   "install <runtime>",
		Short: "Declare the stdio server in one runtime's configuration",
		Long: "Writes one entry, `roca mcp serve` over stdio, into the runtime's own\n" +
			"configuration file. Everything else in that file, comments included, is\n" +
			"left exactly as it was, and the previous bytes are backed up first.\n\n" +
			"Supported runtimes: " + listOfRuntimes(),
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := configFileOf(args[0], configPath)
			if err != nil {
				return err
			}
			declared := chosenExecutable(executable)
			if !filepath.IsAbs(declared) {
				return fmt.Errorf("resolve the running executable %q to an absolute path", declared)
			}
			var rollback func() error
			if args[0] == agentcfg.RuntimeZcode {
				rollback, err = env.recordZcodeMCPPreimage(path)
				if err != nil {
					return err
				}
			}
			outcome, err := agentcfg.Install(args[0], path, declared)
			if err != nil {
				if rollback != nil {
					err = errors.Join(err, rollback())
				}
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{
					"runtimes": []agentcfg.Outcome{outcome}, "executable": declared,
				})
			}
			if outcome.Changed {
				env.print("%s: wrote MCP server %q to %s", outcome.Runtime,
					agentcfg.ServerName, outcome.Path)
			} else {
				env.print("%s: MCP server %q already declared in %s; left unchanged",
					outcome.Runtime, agentcfg.ServerName, outcome.Path)
			}
			env.print("command: %s mcp serve", declared)
			if outcome.Backup != "" {
				env.print("backup: %s", outcome.Backup)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "the configuration file to edit")
	cmd.Flags().StringVar(&executable, "executable", "",
		"the binary the entry launches (default: this executable; override with "+EnvExecutable+")")
	return cmd
}

func mcpUninstallCommand(env *cliEnv) *cobra.Command {
	var configPath string
	var all bool
	cmd := &cobra.Command{
		Use:   "uninstall [runtime]",
		Short: "Withdraw La Roca's entry, leaving the rest of the file as it was",
		Long: "The reversion `roca uninstall` performs over every runtime, available on\n" +
			"its own so an operator can undo one integration without touching their data.\n\n" +
			"Supported runtimes: " + listOfRuntimes(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name one runtime (%s) or ask for --all",
					listOfRuntimes())
			}
			runtimes := args
			if all {
				runtimes = agentcfg.Runtimes()
			}
			if err := oneRuntimeForAConfigPath(configPath, len(runtimes)); err != nil {
				return err
			}
			var outcomes []agentcfg.Outcome
			for _, runtime := range runtimes {
				path, err := configFileOf(runtime, configPath)
				if err != nil {
					return err
				}
				outcome, err := env.uninstallMCP(runtime, path)
				if err != nil {
					return err
				}
				outcomes = append(outcomes, outcome)
			}
			return env.renderOutcomes(outcomes, "withdrawn")
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "the configuration file to edit")
	cmd.Flags().BoolVar(&all, "all", false, "withdraw from every supported runtime")
	return cmd
}

func mcpStatusCommand(env *cliEnv) *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "status [runtime]",
		Short: "Which agents have La Roca configured, and which binary they launch",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := oneRuntimeForAConfigPath(configPath, len(args)); err != nil {
				return err
			}
			return runtimeStatus(env, args, agentcfg.Runtimes(),
				func(runtime string) (agentcfg.Report, error) {
					path, err := configFileOf(runtime, configPath)
					if err != nil {
						return agentcfg.Report{}, err
					}
					return agentcfg.Status(runtime, path)
				},
				[]string{"runtime", "state", "detail"}, func(report agentcfg.Report) map[string]any {
					return map[string]any{"runtime": report.Runtime, "state": report.State,
						"detail": firstNonEmpty(report.Instance, report.Error, report.Path)}
				},
				"Run `roca mcp install <runtime>` to configure one runtime",
				"Run `roca mcp uninstall <runtime>` to withdraw one declaration")
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "the configuration file to read")
	return cmd
}

const zcodeMCPPreimageFormat = "zcode-mcp-preimage-v1:"

func (env *cliEnv) recordZcodeMCPPreimage(path string) (func() error, error) {
	registryPath, registry, err := env.artifactRegistry()
	if err != nil {
		return nil, err
	}
	if entry, found := registry.Find(artifactKindMCP, agentcfg.RuntimeZcode, path); found {
		status, statusErr := agentcfg.Status(agentcfg.RuntimeZcode, path)
		if statusErr != nil {
			return nil, statusErr
		}
		if status.State == agentcfg.StateConfigured {
			if _, err := zcodeMCPPreimageFromEntry(entry); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		body = nil
	} else if err != nil {
		return nil, err
	}
	preimage, err := agentcfg.ZcodeMCPPreimage(string(body))
	if err != nil {
		return nil, err
	}
	before := registry
	before.Entries = append([]artifact.Entry(nil), registry.Entries...)
	_, statErr := os.Stat(registryPath)
	registryExisted := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, statErr
	}
	registry.Upsert(artifact.Entry{
		Kind: artifactKindMCP, Runtime: agentcfg.RuntimeZcode, Path: path,
		InstalledVersion: env.build.Version, AvailableVersion: env.build.Version,
		Format: zcodeMCPPreimageFormat + preimage,
	})
	if err := artifact.SaveRegistry(registryPath, registry); err != nil {
		return nil, err
	}
	return func() error {
		if registryExisted {
			return artifact.SaveRegistry(registryPath, before)
		}
		if err := os.Remove(registryPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}, nil
}

func zcodeMCPPreimageFromEntry(entry artifact.Entry) (string, error) {
	if !strings.HasPrefix(entry.Format, zcodeMCPPreimageFormat) {
		return "", fmt.Errorf("ZCode MCP ownership state for %s has unknown format %q", entry.Path, entry.Format)
	}
	preimage := strings.TrimPrefix(entry.Format, zcodeMCPPreimageFormat)
	if preimage != agentcfg.ZcodeMCPPreimageNone && preimage != agentcfg.ZcodeMCPPreimageServers &&
		preimage != agentcfg.ZcodeMCPPreimageMCPServers {
		return "", fmt.Errorf("ZCode MCP ownership state for %s has invalid preimage %q", entry.Path, preimage)
	}
	return preimage, nil
}

func (env *cliEnv) uninstallMCP(runtime, path string) (agentcfg.Outcome, error) {
	if runtime != agentcfg.RuntimeZcode {
		return agentcfg.Uninstall(runtime, path)
	}
	preimage := agentcfg.ZcodeMCPPreimageNone
	entry, found, err := env.registeredArtifact(artifactKindMCP, runtime, path)
	if err != nil {
		return agentcfg.Outcome{Runtime: runtime, Path: path}, err
	}
	if found {
		preimage, err = zcodeMCPPreimageFromEntry(entry)
		if err != nil {
			return agentcfg.Outcome{Runtime: runtime, Path: path}, err
		}
	}
	outcome, err := agentcfg.UninstallZcodeMCP(path, preimage)
	if err != nil {
		return outcome, err
	}
	if found {
		err = env.unregisterArtifact(artifactKindMCP, runtime, path)
	}
	return outcome, err
}

func (env *cliEnv) renderOutcome(outcome agentcfg.Outcome, verb string) error {
	return env.renderOutcomes([]agentcfg.Outcome{outcome}, verb)
}

func (env *cliEnv) renderOutcomes(outcomes []agentcfg.Outcome, verb string) error {
	if env.json {
		return env.printJSON(map[string]any{"runtimes": outcomes})
	}
	for _, outcome := range outcomes {
		done := "unchanged"
		if outcome.Changed {
			done = verb
		}
		backup := ""
		if outcome.Backup != "" {
			backup = " (backup: " + outcome.Backup + ")"
		}
		env.print("%s: %s %s%s", outcome.Runtime, done, outcome.Path, backup)
	}
	return nil
}

// oneRuntimeForAConfigPath refuses a declared configuration file when more than
// one runtime is selected. `--config` names ONE runtime's file, and applying it
// to every runtime edited that single file once per runtime, each pass with a
// different agent's rules: one agent's configuration rewritten by another's
// editor.
func oneRuntimeForAConfigPath(configPath string, runtimes int) error {
	if configPath == "" || runtimes == 1 {
		return nil
	}
	return fmt.Errorf(
		"--config names one runtime's file: name that one runtime (%s) instead of all of them",
		listOfRuntimes())
}

// configFileOf resolves where a runtime keeps its configuration, unless the
// operator named the file themselves. A test sandbox and a machine with an
// unusual layout both need that flag.
func configFileOf(runtime, declared string) (string, error) {
	if runtime == agentcfg.RuntimeClaudeDesktop {
		if _, err := agentcfg.ConfigPath(runtime, "", os.Getenv); err != nil {
			return "", err
		}
	}
	if declared != "" {
		return declared, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is: name the file with --config")
	}
	return agentcfg.ConfigPath(runtime, home, os.Getenv)
}

func chosenExecutable(declared string) string {
	if override := firstNonEmpty(declared, os.Getenv(EnvExecutable)); override != "" {
		absolute, err := filepath.Abs(override)
		if err == nil {
			return absolute
		}
		return override
	}
	running, err := os.Executable()
	if err != nil {
		return "roca"
	}
	absolute, err := filepath.Abs(running)
	if err != nil {
		return running
	}
	return absolute
}

func listOfRuntimes() string { return strings.Join(agentcfg.Runtimes(), ", ") }

// firstNonEmpty is the first of these values that says something.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
