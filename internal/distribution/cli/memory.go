package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/axi"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
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
					"installed": false, "plugin": rocaops.Name, "reason": reason,
				}, "%s: the bundled %s plugin was not placed", reason, rocaops.Name)
			}
			result, err := rocaops.Ensure(root, pluginExecutableDir(paths), env.build.Version)
			if err != nil {
				return err
			}
			return env.report(map[string]any{
				"installed": true, "plugin": result.Name, "version": result.Version,
				"risk": result.Risk, "resident": true,
			}, "bundled plugin %s %s at %s", result.Name, result.Version, result.Directory)
		},
	}
}

func opsCommand(env *cliEnv) *cobra.Command {
	command := &cobra.Command{
		Use:   "ops",
		Short: "Operate the experimental resident operational store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	command.AddCommand(opsDrainCommand(env))
	return command
}

func opsDrainCommand(env *cliEnv) *cobra.Command {
	var before string
	command := &cobra.Command{
		Use:   "drain",
		Short: "Remove operational rows whose explicit expiry is due",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cutoff := time.Now().UTC()
			if before != "" {
				var err error
				cutoff, err = time.Parse(time.RFC3339, before)
				if err != nil {
					return fmt.Errorf("--before must be RFC3339: %w", err)
				}
			}
			svc, _, err := env.openService()
			if err != nil {
				return err
			}
			defer svc.Close()
			result, err := svc.DrainRocaOps(cmd.Context(), cutoff)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(result)
			}
			env.print("roca-ops drain: %d expired memories removed before %s",
				result.Removed, result.Before)
			return nil
		},
	}
	command.Flags().StringVar(&before, "before", "", "RFC3339 cutoff (default: now)")
	return command
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
