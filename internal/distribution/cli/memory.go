package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacron"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocavector"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	pluginstd "github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

// storeCommand is the write half of the product on the shell. It is the same
// service call the plug's `roca_store` makes, with `surface` telling them apart
// afterwards.
func storeCommand(env *cliEnv) *cobra.Command {
	var req service.StoreRequest
	var metadata string
	var agent, model string
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Write one memory",
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, _, err := env.openStoreService()
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
			req.Authorship = resolveCLIAuthorship(agent, model, currentAuthorshipEvidence)
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
	cmd.Flags().StringVar(&agent, "agent", "", "writing agent harness (primary CLI identity path)")
	cmd.Flags().StringVar(&model, "model", "", "writing model (primary CLI identity path)")
	cmd.Flags().StringVar(&req.Project, "project", "", "project scope (omit for global)")
	cmd.Flags().StringVar(&req.Status, "status", "", "active, pending or resolved")
	cmd.Flags().Int64Var(&req.Supersedes, "supersedes", 0, "id of the memory this one replaces")
	cmd.Flags().StringVar(&metadata, "metadata", "", "structured tags, as a JSON object")
	cmd.MarkFlagRequired("layer")
	cmd.MarkFlagRequired("content")
	return cmd
}

func installBundledPluginsCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:    "_install-bundled-plugins",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			paths, pathErr := env.resolvePaths()
			root := ""
			if pathErr == nil {
				root = pluginRoot(paths)
			}
			// A machine with no home has nowhere to keep a bundled plugin, and
			// the feature that reads it refuses to run there anyway. Saying so is
			// the answer; failing the installation that just succeeded is not.
			if root == "" {
				reason := "no home directory"
				if pathErr != nil {
					reason = pathErr.Error()
				}
				return env.report(map[string]any{
					"installed": false,
					"plugins": []string{
						rocaops.Name, rocacorpus.Name, rocacron.Name, rocavector.Name,
					},
					"reason": reason,
				}, "%s: bundled plugins were not placed", reason)
			}
			binDir := pluginExecutableDir(paths)
			ops, err := rocaops.Ensure(root, binDir, env.build.Version)
			if err != nil {
				return err
			}
			corpus, err := rocacorpus.Ensure(root, binDir, env.build.Version)
			if err != nil {
				return err
			}
			cron, err := rocacron.Ensure(root, binDir, env.build.Version)
			if err != nil {
				return err
			}
			vector, err := rocavector.Ensure(root, binDir, env.build.Version)
			if err != nil {
				return err
			}
			return env.report(map[string]any{
				"installed": true, "plugins": []any{
					map[string]any{"name": ops.Name, "version": ops.Version, "risk": ops.Risk, "resident": true},
					map[string]any{"name": corpus.Name, "version": corpus.Version, "risk": corpus.Risk, "resident": true},
					map[string]any{"name": cron.Name, "version": cron.Version, "risk": cron.Risk, "resident": false},
					map[string]any{"name": vector.Name, "version": vector.Version, "risk": vector.Risk, "resident": false},
				},
			}, "bundled plugins %s, %s, %s and %s installed",
				ops.Name, corpus.Name, cron.Name, vector.Name)
		},
	}
}

func cronCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "cron",
		Short: "Run and inspect plugin ride trains",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(cronListCommand(env), cronRunCommand(env))
	return command
}

func cronListCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered rides and their trains",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			service, err := env.openCronService(config.ReadOnly(os.Getenv(config.EnvReadOnly)))
			if err != nil {
				return err
			}
			defer service.Close()
			rides, warnings := service.List()
			for _, warning := range warnings {
				fmt.Fprintf(env.errOut, "warning: %s\n", warning)
			}
			if env.json {
				return env.printJSON(map[string]any{"rides": rides, "warnings": warnings})
			}
			for _, ride := range rides {
				gate := ride.Gate
				if gate == "" {
					gate = "-"
				}
				env.print("%s\t%s\t%s\t%s\t%s", ride.Plugin, ride.Name, ride.Train, gate, ride.Command)
			}
			return nil
		},
	}
}

func cronRunCommand(env *cliEnv) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use:   "run [train]",
		Short: "Run one train or preview its gates",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			readOnly := config.ReadOnly(os.Getenv(config.EnvReadOnly))
			if readOnly && !dryRun {
				return fmt.Errorf("%s refuses cron runs because they record journeys", config.EnvReadOnly)
			}
			service, err := env.openCronService(readOnly)
			if err != nil {
				return err
			}
			defer service.Close()
			train := pluginstd.DefaultTrain
			if len(args) == 1 {
				train = args[0]
			}
			report, err := service.Run(cmd.Context(), train, dryRun)
			if err != nil {
				return err
			}
			if env.json {
				if report.Failed > 0 {
					env.code = ExitError
				}
				return env.printJSON(report)
			}
			for _, ride := range report.Rides {
				exit := "-"
				if ride.ExitCode != nil {
					exit = fmt.Sprint(*ride.ExitCode)
				}
				env.print("%s\t%s\t%s\texit=%s\t%s", ride.Plugin, ride.Ride,
					ride.GateStatus, exit, ride.Command)
			}
			env.print("train %s: %d rides, %d failed, %d deferred",
				report.Train, len(report.Rides), report.Failed, report.Deferred)
			if report.Failed > 0 {
				env.code = ExitError
			}
			return nil
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"preview ride order and gate status without executing or recording")
	return command
}

func (env *cliEnv) openCronService(readOnly bool) (*rocacron.Service, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, err
	}
	root := pluginRoot(paths)
	if root == "" {
		return nil, fmt.Errorf("I do not know where your HOME is; %s needs ~/.roca/plugins", rocacron.Name)
	}
	if !readOnly {
		if _, err := rocacron.Ensure(root, pluginExecutableDir(paths), env.build.Version); err != nil {
			return nil, fmt.Errorf("install bundled %s plugin: %w", rocacron.Name, err)
		}
	}
	out, errOut := io.Writer(env.out), io.Writer(env.errOut)
	if env.json {
		out, errOut = io.Discard, io.Discard
	}
	return rocacron.Open(rocacron.Options{
		PluginRoot: root,
		Database:   filepath.Join(root, rocacron.Name, rocacron.DatabaseFilename),
		LockPath:   logfile.New(filepath.Dir(paths.DB)).LockPath(),
		ReadOnly:   readOnly,
		Out:        out,
		ErrOut:     errOut,
	})
}

type authorshipProcess struct {
	Command   string
	Arguments []string
}

type authorshipEvidence struct {
	Environment map[string]string
	Processes   []authorshipProcess
}

// resolveCLIAuthorship reads the evidence only when it can still change the
// answer. The documented path passes both flags, and gathering process ancestry
// to discard it costs a subprocess per generation of the tree on every write.
func resolveCLIAuthorship(agent, model string, evidence func() authorshipEvidence) service.Authorship {
	resolvedAgent := strings.TrimSpace(agent)
	resolvedModel := strings.TrimSpace(model)
	if resolvedAgent != "" && resolvedModel != "" {
		return service.Authorship{
			Agent: resolvedAgent, Model: resolvedModel, Surface: service.SurfaceCLI,
		}
	}
	detectedAgent, detectedModel := detectCLIAuthorship(evidence())
	if resolvedAgent == "" {
		resolvedAgent = detectedAgent
	}
	if resolvedModel == "" && resolvedAgent == detectedAgent {
		resolvedModel = detectedModel
	}
	if resolvedAgent == "" {
		resolvedAgent = service.UnknownAuthor
	}
	if resolvedModel == "" {
		resolvedModel = service.UnknownAuthor
	}
	return service.Authorship{Agent: resolvedAgent, Model: resolvedModel, Surface: service.SurfaceCLI}
}

func detectCLIAuthorship(evidence authorshipEvidence) (string, string) {
	candidates := map[string]bool{}
	markers := map[string]bool{
		"claude": evidence.Environment["CLAUDECODE"] == "1",
		"codex":  strings.TrimSpace(evidence.Environment["CODEX_THREAD_ID"]) != "",
		"hermes": strings.TrimSpace(evidence.Environment["HERMES_SESSION_ID"]) != "",
	}
	// OpenCode and Pi expose no unambiguous tool-subprocess identity marker.
	// They are attributed only when the process ancestry names the harness.
	for agent, present := range markers {
		if present {
			candidates[agent] = true
		}
	}
	for _, process := range evidence.Processes {
		if agent := harnessFromProcess(process); agent != "" {
			candidates[agent] = true
		}
	}
	if len(candidates) != 1 {
		return service.UnknownAuthor, service.UnknownAuthor
	}
	var agent string
	for candidate := range candidates {
		agent = candidate
	}
	model := modelFromEvidence(agent, evidence)
	if model == "" {
		model = service.UnknownAuthor
	}
	return agent, model
}

func harnessFromProcess(process authorshipProcess) string {
	names := []string{process.Command}
	if len(process.Arguments) > 0 {
		names = append(names, process.Arguments[0])
	}
	for _, raw := range names {
		name := strings.ToLower(filepath.Base(raw))
		switch {
		case name == "claude" || name == "claude-code":
			return "claude"
		case name == "codex" || strings.HasPrefix(name, "codex-"):
			return "codex"
		case name == "opencode":
			return "opencode"
		case name == "pi" || name == "pi-signed" || name == "pi-launcher":
			return "pi"
		case name == "hermes" || name == "hermes-agent":
			return "hermes"
		}
	}
	return ""
}

func modelFromEvidence(agent string, evidence authorshipEvidence) string {
	models := map[string]bool{}
	key := map[string]string{"hermes": "HERMES_INFERENCE_MODEL"}[agent]
	if model := strings.TrimSpace(evidence.Environment[key]); model != "" {
		models[model] = true
	}
	for _, process := range evidence.Processes {
		if harnessFromProcess(process) != agent {
			continue
		}
		for i, argument := range process.Arguments {
			switch {
			case argument == "--model" && i+1 < len(process.Arguments):
				models[strings.TrimSpace(process.Arguments[i+1])] = true
			case strings.HasPrefix(argument, "--model="):
				models[strings.TrimSpace(strings.TrimPrefix(argument, "--model="))] = true
			}
		}
	}
	delete(models, "")
	if len(models) != 1 {
		return ""
	}
	for model := range models {
		return model
	}
	return ""
}

func currentAuthorshipEvidence() authorshipEvidence {
	keys := []string{
		"CLAUDECODE", "CODEX_THREAD_ID",
		"HERMES_SESSION_ID", "HERMES_INFERENCE_MODEL",
	}
	environment := make(map[string]string, len(keys))
	for _, key := range keys {
		environment[key] = os.Getenv(key)
	}
	return authorshipEvidence{Environment: environment, Processes: processAncestry(os.Getppid())}
}

func processAncestry(pid int) []authorshipProcess {
	processes := make([]authorshipProcess, 0, 8)
	for range 8 {
		output, err := exec.Command("ps", "-o", "ppid=", "-o", "comm=", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			break
		}
		fields := strings.Fields(string(output))
		if len(fields) < 2 {
			break
		}
		parent, err := strconv.Atoi(fields[0])
		if err != nil {
			break
		}
		processes = append(processes, authorshipProcess{Command: fields[1], Arguments: fields[2:]})
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return processes
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

func memoryCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{Use: "memory", Short: "Resolve durable memory identities"}
	command.AddCommand(&cobra.Command{
		Use:   "resolve <id>",
		Short: "Resolve a memory id or exact-dedup alias to its canonical row",
		Args:  cobra.ExactArgs(1),
		RunE: env.serviceRunE(func(cmd *cobra.Command, args []string, svc *service.Service) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("memory id must be a positive integer")
			}
			result, err := svc.ResolveMemory(cmd.Context(), id)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(result)
			}
			env.print("memory: requested=%d canonical=%d alias=%t database=%s layer=%s",
				result.RequestedID, result.CanonicalID, result.Alias, result.Database, result.Layer)
			return nil
		}),
	})
	return command
}
