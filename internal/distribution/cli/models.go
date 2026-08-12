package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/distribution/logfile"
	"github.com/thellmwhisperer/la-roca/internal/distribution/reconcile"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
)

const (
	doctorQueryFailureWindow = 24 * time.Hour
	doctorQueryFailureLimit  = 5
)

type doctorReport struct {
	service.DoctorReport
	QueryFailures logfile.QueryFailureSummary `json:"query_failures"`
}

func doctorCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the configuration and which model is going to answer",
		Long: "Reports where the data and the configuration are, which providers this\n" +
			"installation declares, which of them are available and, for the ones that\n" +
			"are not, the exact command that fixes it. Local agent models authenticate\n" +
			"through their own CLIs; La Roca stores no secrets.",
		RunE: env.serviceRunE(func(cmd *cobra.Command, _ []string, svc *service.Service) error {
			report, err := svc.Doctor(cmd.Context())
			if err != nil {
				return err
			}
			proposals, err := env.openCapabilityProposals()
			if err != nil {
				return err
			}
			for _, proposal := range proposals {
				report.CapabilityProposals = append(report.CapabilityProposals, proposal.Proposal.Alert)
			}
			failures, logErr := logfile.New(svc.DataDir()).RecentQueryFailures(
				time.Now(), doctorQueryFailureWindow, doctorQueryFailureLimit)
			if logErr != nil {
				report.Warnings = append(report.Warnings,
					"query failure log could not be read: "+logErr.Error())
			}
			answer := doctorReport{DoctorReport: report, QueryFailures: failures}
			if env.json {
				return env.printJSON(answer)
			}
			renderDoctor(env, report)
			renderQueryFailures(env, failures)
			if terminalInput(cmd.InOrStdin()) && !env.skipReconciliation {
				_, err = env.reconcileCapabilities(cmd, true, true)
			}
			return err
		}),
	}
}

func renderQueryFailures(env *cliEnv, summary logfile.QueryFailureSummary) {
	env.print("query failures (last 24h): %d", summary.Count)
	if len(summary.Recent) == 0 {
		return
	}
	rows := make([]map[string]any, 0, len(summary.Recent))
	for _, failure := range summary.Recent {
		rows = append(rows, map[string]any{
			"time":   failure.Timestamp.UTC().Format(time.RFC3339),
			"source": failure.Source, "call": failure.Operation,
			"type": failure.ErrorType, "error": failure.Error,
			"correlation_id": failure.CorrelationID,
		})
	}
	env.print("%s", rowOutput(
		[]string{"time", "source", "call", "type", "error", "correlation_id"}, rows))
}

func renderDoctor(env *cliEnv, report service.DoctorReport) {
	env.print("roca %s (%s)", report.Version, report.SourceSHA)
	env.print("database: %s · %d memories", report.DBPath, report.Memories)
	renderBedrock(env, report.Bedrock)
	if report.ConfigExists {
		env.print("configuration: %s", report.ConfigPath)
	} else {
		env.print("configuration: %s (does not exist: defaults in use)", report.ConfigPath)
	}
	env.print("agents detected: %s", detectedAgentsLine(report.DetectedAgents))
	env.print("agents not found: %s", missingAgentsLine(report.DetectedAgents))
	renderModelDetection(env, report.DetectedModelBinaries, report.MissingModelBinaries,
		report.FactoryDefault, report.FactoryDefaultProvider)
	env.print("authentication: local agent models use their own CLI sessions; La Roca stores no secrets")

	for _, warning := range report.Warnings {
		env.print("warning: %s", warning)
	}

	switch {
	case report.ModelDisabled && len(report.Providers) == 0:
		env.print("model: turned off by configuration")
	case len(report.Providers) == 0:
		env.print("model: no provider declared")
	default:
		env.print("providers, in the declared order:")
		renderProviders(env, report.ConfigPath, report.Providers)
	}

	if report.Titular != "" {
		env.print("the one that is going to answer: %s", report.Titular)
	} else if len(report.Providers) > 0 {
		env.print("no provider is available: questions the compiler does not resolve " +
			"will fall to the keyword rescue")
	}
	renderInterpretation(env, report)
	if report.PromptPath != "" {
		if report.PromptExists {
			env.print("agent prompt: %s (paste it into agent instructions)", report.PromptPath)
		} else {
			env.print("agent prompt: missing at %s", report.PromptPath)
			env.print("      remedy: run `roca init` to generate it")
		}
	}
	if len(report.CapabilityProposals) > 0 {
		env.print("open capability proposals:")
		for _, proposal := range report.CapabilityProposals {
			env.print("  - %s", proposal)
		}
	}
}

// renderProviders is one declared order with a verdict per provider, and it is
// the same block for both of them: the order that answers questions and the one
// that reads the result rows are read by the same operator, in the same shape.
func renderProviders(env *cliEnv, configPath string, providers []service.DoctorProvider) {
	for _, p := range providers {
		status := "working"
		if !p.Ready {
			status = "failed"
		}
		env.print("  %s %s · model %s (%s · change with: %s) · probe %s",
			env.mark(p.Ready), p.Name, orDash(p.Model), modelChoiceSource(configPath, p.Name, p.Model),
			modelChange(p.Name, configPath), status)
		if !p.Ready {
			env.print("      %s", orDash(p.Reason))
			if p.Action != "" {
				env.print("      remedy: %s", p.Action)
			}
		}
	}
}

// renderInterpretation is the second inference's decision, and it is printed
// only by an installation that declared one. With the split active the result
// rows go to that provider and to no other, which is the whole reason to
// declare it, so the line says it out loud.
func renderInterpretation(env *cliEnv, report service.DoctorReport) {
	if len(report.Interpreters) == 0 {
		return
	}
	env.print("interpretation providers, in the declared order:")
	renderProviders(env, report.ConfigPath, report.Interpreters)
	if report.InterpretTitular != "" {
		env.print("the one that is going to read the result rows: %s "+
			"(the rows go to it and to no other provider)", report.InterpretTitular)
		return
	}
	env.print("no interpretation provider is available: the result rows fall back to " +
		"the provider that writes the SQL")
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

type initModelOrigin struct {
	Provider string
	Label    string
	Models   []string
	Open     bool
}

type initModelChoice struct {
	Provider string
	Model    string
}

func (env *cliEnv) chooseInitModel(ctx context.Context, input *bufio.Reader,
	paths config.Paths, result service.InitResult) (service.InitResult, bool, error) {
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return result, false, err
	}
	origins := env.discoverInitModels(ctx, paths, file)
	defaultChoice := currentInitModel(result)
	origins = includeCurrentInitModel(origins, defaultChoice)
	if defaultChoice.Model == "" {
		defaultChoice = firstInitModel(origins)
	}
	if defaultChoice.Model == "" {
		return result, true, nil
	}
	model, err := env.askInitModel(input, origins, defaultChoice.Model)
	if err != nil {
		return result, false, err
	}
	candidates := initHarnesses(origins, model)
	if len(candidates) == 0 {
		return result, false, fmt.Errorf(
			"no detected harness can serve model %s; configuration was not changed", model)
	}
	harness, err := env.askInitHarness(input, model, candidates, defaultChoice.Provider)
	if err != nil {
		return result, false, err
	}
	confirmed, err := env.confirmInitModel(input, env.errOut, harness, model)
	if err != nil {
		return result, false, err
	}
	if !confirmed {
		env.initSay("model choice canceled; configuration was not changed")
		return result, false, nil
	}
	if harness != defaultChoice.Provider || model != defaultChoice.Model {
		backend := env.initModelBackend(paths, file, harness)
		if err := backend.Probe(ctx, harness, model); err != nil {
			return result, false, fmt.Errorf(
				"%s model %s failed its account probe: %w; configuration was not changed",
				harness, model, err)
		}
	}
	if err := env.offerRetirementFor(input, harness); err != nil {
		return result, false, err
	}
	outcome, err := writeInitModelChoice(paths, harness, model)
	if err != nil {
		return result, false, err
	}
	if outcome.Changed {
		fmt.Fprintf(env.errOut, "configuration updated: %s", outcome.Path)
		if outcome.Backup != "" {
			fmt.Fprintf(env.errOut, " (backup: %s)", outcome.Backup)
		}
		fmt.Fprintln(env.errOut)
	} else {
		env.initSay("configuration unchanged: %s", outcome.Path)
	}
	result.Model, err = effectiveInitModel(ctx, paths)
	if err != nil {
		return result, false, err
	}
	if !result.Model.Ready {
		return result, false, fmt.Errorf(
			"the persisted model choice has no available provider: %s", result.Model.Reason)
	}
	result.FactoryDefault = false
	result.FactoryDefaultProvider = ""
	return result, true, nil
}

func effectiveInitModel(ctx context.Context, paths config.Paths) (*service.InitModel, error) {
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return nil, err
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve the effective model choice from %s: %w", paths.Config, err)
	}
	if cascade.Disabled {
		return &service.InitModel{Disabled: true, Reason: "the model is turned off in the configuration"}, nil
	}
	if len(cascade.Providers) == 0 {
		return &service.InitModel{
			Reason: "no model provider is configured",
			Action: "declare one under [models] in " + paths.Config,
		}, nil
	}
	gate := &service.InitModel{}
	for index, attempt := range cascade.Diagnose(ctx) {
		if attempt.Ready {
			transport, isCommand := cascade.Providers[index].(interface{ CommandTransport() bool })
			return &service.InitModel{
				Ready: true, Provider: attempt.Name, Model: attempt.ModelID,
				CommandTransport: isCommand && transport.CommandTransport(),
			}, nil
		}
		if gate.Reason == "" {
			gate.Provider, gate.Reason, gate.Action = attempt.Name, attempt.Reason, attempt.Action
		}
	}
	return gate, nil
}

func (env *cliEnv) discoverInitModels(ctx context.Context, paths config.Paths,
	file config.File) []initModelOrigin {
	var origins []initModelOrigin
	for _, name := range provider.DetectedCommandPresets(nil) {
		model, ok := provider.CommandPresetDefaultModel(name)
		if ok && model != "" {
			origins = append(origins, initModelOrigin{
				Provider: name, Label: "detected CLI", Models: []string{model}, Open: true,
			})
		}
	}
	catalogue, err := env.initModelBackend(paths, file, provider.NameOllama).
		Catalogue(ctx, provider.NameOllama, file.Models.Providers[provider.NameOllama].Model)
	if err == nil {
		origins = append(origins, initModelOrigin{
			Provider: provider.NameOllama, Label: "locally pulled",
			Models: canonicalModelIDs(catalogue.IDs),
		})
	} else {
		env.initSay("  ollama (locally pulled): unavailable (%v)", err)
	}
	return origins
}

func (env *cliEnv) initModelBackend(paths config.Paths, file config.File,
	name string) modelValidationBackend {
	if env.modelBackend != nil {
		return env.modelBackend
	}
	file.Models.Providers = cloneProviderConfigs(file.Models.Providers)
	cfg := file.Models.Providers[name]
	if slices.Contains(provider.CommandPresetNames(), name) {
		cfg.BaseURL = ""
		cfg.Command = nil
	}
	if name == provider.NameOllama {
		cfg.Command = nil
	}
	file.Models.Providers[name] = cfg
	return newProviderModelBackend(paths, file)
}

func currentInitModel(result service.InitResult) initModelChoice {
	if result.Model == nil || !result.Model.Ready {
		return initModelChoice{}
	}
	return initModelChoice{Provider: result.Model.Provider, Model: result.Model.Model}
}

func includeCurrentInitModel(origins []initModelOrigin, choice initModelChoice) []initModelOrigin {
	if choice.Model == "" {
		return origins
	}
	for index := range origins {
		if origins[index].Provider == choice.Provider {
			if !slices.Contains(origins[index].Models, choice.Model) {
				origins[index].Models = append(origins[index].Models, choice.Model)
			}
			return origins
		}
	}
	return append(origins, initModelOrigin{
		Provider: choice.Provider, Label: "currently selected", Models: []string{choice.Model},
	})
}

func firstInitModel(origins []initModelOrigin) initModelChoice {
	for _, origin := range origins {
		if len(origin.Models) > 0 {
			return initModelChoice{Provider: origin.Provider, Model: origin.Models[0]}
		}
	}
	return initModelChoice{}
}

func (env *cliEnv) askInitModel(input *bufio.Reader, origins []initModelOrigin,
	defaultModel string) (string, error) {
	env.initSay("model chooser:")
	var numbered []string
	seen := map[string]bool{}
	open := false
	for _, origin := range origins {
		env.initSay("  %s (%s):", origin.Provider, origin.Label)
		if len(origin.Models) == 0 {
			env.initSay("    no models reported")
		}
		for _, model := range origin.Models {
			if !seen[model] {
				numbered = append(numbered, model)
				seen[model] = true
			}
			marker := ""
			if model == defaultModel {
				marker = " (default)"
			}
			env.initSay("    %d. %s%s", slices.Index(numbered, model)+1, model, marker)
		}
		open = open || origin.Open
	}
	if open {
		env.initSay("  free text: type any model ID")
	}
	fmt.Fprintf(env.errOut, "Which model do you want answering? [%s]: ", defaultModel)
	answer, err := env.readInitLine(input)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return defaultModel, nil
	}
	if number, parseErr := strconv.Atoi(answer); parseErr == nil && number > 0 && number <= len(numbered) {
		return numbered[number-1], nil
	}
	return answer, nil
}

func initHarnesses(origins []initModelOrigin, model string) []string {
	var exact []string
	for _, origin := range origins {
		if slices.Contains(origin.Models, model) {
			exact = append(exact, origin.Provider)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	var open []string
	for _, origin := range origins {
		if origin.Open {
			open = append(open, origin.Provider)
		}
	}
	return open
}

func (env *cliEnv) askInitHarness(input *bufio.Reader, model string, candidates []string,
	defaultProvider string) (string, error) {
	if len(candidates) == 1 {
		env.initSay("Harness: %s (only detected harness for %s)", candidates[0], model)
		return candidates[0], nil
	}
	if !slices.Contains(candidates, defaultProvider) {
		defaultProvider = candidates[0]
	}
	env.initSay("Detected harnesses for %s:", model)
	for index, candidate := range candidates {
		env.initSay("  %d. %s", index+1, candidate)
	}
	fmt.Fprintf(env.errOut, "Which harness serves %s? [%s]: ", model, defaultProvider)
	answer, err := env.readInitLine(input)
	if err != nil {
		return "", err
	}
	if answer == "" {
		return defaultProvider, nil
	}
	if number, parseErr := strconv.Atoi(answer); parseErr == nil && number > 0 && number <= len(candidates) {
		return candidates[number-1], nil
	}
	answer = strings.ToLower(answer)
	if slices.Contains(candidates, answer) {
		return answer, nil
	}
	return "", fmt.Errorf("harness %q is not one of %s; configuration was not changed",
		answer, strings.Join(candidates, ", "))
}

func (env *cliEnv) confirmInitModel(input *bufio.Reader, out io.Writer,
	providerName, model string) (bool, error) {
	fmt.Fprintf(out, "Use %s/%s? [Y/n]: ", providerName, model)
	answer, err := env.readInitLine(input)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("confirmation %q is not valid; answer yes or no", answer)
	}
}

func (env *cliEnv) readInitLine(input *bufio.Reader) (string, error) {
	line, err := env.readInitRaw(input)
	answer := strings.TrimSpace(line)
	if err != nil && answer == "" {
		return "", fmt.Errorf("roca init received no answer")
	}
	return answer, nil
}

// writeInitModelChoice persists the pair the operator confirmed and nothing
// else. Retiring a legacy transport is a separate, visible accept/decline
// proposal: a model-selection prompt never deletes an operator's settings or
// the credential files an older release left behind.
func writeInitModelChoice(paths config.Paths, providerName, model string) (agentcfg.Outcome, error) {
	changes := []config.Change{
		{Kind: config.PrependUnique, Table: "models", Key: "order", Value: providerName,
			Default: provider.DefaultOrder(nil)},
		{Kind: config.SetValue, Table: "models." + providerName, Key: "model", Value: model},
	}
	return agentcfg.EditWithBackup("roca", paths.Config, func(text string) (string, error) {
		return config.ApplyText(text, changes)
	}, config.RedactProviderSecrets, true)
}

// offerRetirementFor puts the reconciliation proposals that concern the chosen
// provider in front of the operator, with their own alert and their own yes/no,
// before init writes the choice. It is the only route by which init retires a
// legacy transport, and it is always shown rather than stamped away, because
// the operator has just asked for that provider to answer.
func (env *cliEnv) offerRetirementFor(input *bufio.Reader, providerName string) error {
	if env.skipReconciliation {
		return nil
	}
	context, err := env.reconciliationContext()
	if err != nil {
		return err
	}
	var open []reconcile.Entry
	for _, entry := range reconcile.Open(context, reconcile.Registry()) {
		if entry.RetiredProvider == providerName {
			open = append(open, entry)
		}
	}
	if len(open) == 0 {
		return nil
	}
	_, err = reconcile.Run(context, open, reconcile.Options{
		Interactive: true, ListAll: true, In: input, Out: env.errOut,
	})
	return err
}

const modelsHelp = "" +
	"List the models each configured provider offers, and mark the one that is\n" +
	"going to answer.\n" +
	"\n" +
	"  roca models           # every provider in the declared order\n" +
	"\n" +
	"A provider is asked for its catalogue through its local agent CLI, or\n" +
	"through /api/tags for Ollama. One that does not answer is shown as\n" +
	"unavailable with its reason and the rest keep going: the command never\n" +
	"fails on the first provider that is down. It needs no database, so it\n" +
	"runs before `roca init`."

func modelsCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "List the models each configured provider offers",
		Long:  modelsHelp,
		RunE: func(cmd *cobra.Command, _ []string) error {
			listings, warnings, err := env.resolveModels(cmd)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{
					"version":    env.build.Version,
					"source_sha": env.build.Commit,
					"providers":  listings,
					"warnings":   warnings,
				})
			}
			renderModels(env, listings, warnings)
			return nil
		},
	}
}

// resolveModels builds the cascade from the configuration alone and asks every
// provider for its catalogue. It mirrors the login overview: a question about
// providers, not memory, so it never opens the database and runs before init.
func (env *cliEnv) resolveModels(cmd *cobra.Command) ([]provider.ModelsListing, []string, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil, nil, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return nil, nil, err
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return nil, nil, err
	}
	return cascade.Models(cmd.Context()), cascade.Warnings, nil
}

// renderModels is the readable form of the catalogue. The provider header names
// the model the cascade would use (Selected); the list beneath it shows what the
// command transport or the local runtime reaches, with that model marked. A provider
// that could not be reached shows its reason instead of a list.
func renderModels(env *cliEnv, listings []provider.ModelsListing, warnings []string) {
	for _, warning := range warnings {
		env.print("warning: %s", warning)
	}
	if len(listings) == 0 {
		env.print("no provider is declared")
		return
	}
	env.print("providers, in the declared order:")
	for _, listing := range listings {
		env.print("  %s %s · model %s", env.mark(listing.Ready), listing.Name, orDash(listing.Selected))
		if !listing.Ready {
			env.print("      %s", orDash(listing.Reason))
			continue
		}
		if len(listing.Models) == 0 {
			env.print("      no models reported")
			continue
		}
		for _, model := range listing.Models {
			if model == listing.Selected {
				env.print("      %s (selected)", model)
			} else {
				env.print("      %s", model)
			}
		}
	}
}

const loginHelp = "" +
	"Verify a model provider that runs through a local agent CLI:\n" +
	"\n" +
	"  local CLI  roca login codex\n" +
	"  local CLI  roca login claude\n" +
	"\n" +
	"Models authenticate through their own CLI. La Roca reads and stores no secrets,\n" +
	"and no roca login is required: detected CLIs are used automatically. This\n" +
	"command is an optional probe of the binary and its existing vendor session.\n" +
	"\n" +
	"Choose a model with the arrow keys after a successful probe. Scripts can use\n" +
	"--model <id>; the ID is checked through that CLI before it\n" +
	"is written under [models.<provider>] in ~/.roca/config.toml.\n\n" +
	"With no provider argument, lists the supported local CLIs and model configuration."

func loginCommand(env *cliEnv) *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "login [provider]",
		Short: "Verify a local agent CLI model session",
		Long:  loginHelp,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return env.showLoginOverview()
			}
			name := strings.ToLower(strings.TrimSpace(args[0]))
			paths, err := env.resolvePaths()
			if err != nil {
				return err
			}
			file, err := config.LoadFile(paths.Config)
			if err != nil {
				return err
			}
			if slices.Contains([]string{provider.NameCodex, provider.NameClaude}, name) {
				return env.loginLocalCommand(cmd, paths, file, name, model)
			}
			return fmt.Errorf(
				"there is no La Roca login for %q: models authenticate through their own CLI\n\n%s",
				args[0], loginCatalogue())
		},
	}
	cmd.Flags().StringVar(&model, "model", "", "model ID to persist for this provider")
	return cmd
}

// loginEntry is one row of the catalogue bare `roca login` prints. The same
// list answers the human listing and the JSON under --json, so a provider added
// to one reaches the other instead of agreeing by luck. Flow is the machine
// form; the human listing renders it.
type loginEntry struct {
	Name    string `json:"name"`
	Flow    string `json:"flow"`
	Command string `json:"command"`
}

// loginEntries is the single source of local CLI probes this build supports.
func loginEntries(_ ...config.File) []loginEntry {
	return []loginEntry{{
		Name: provider.NameCodex, Flow: "local_cli",
		Command: "roca login " + provider.NameCodex,
	}, {
		Name: provider.NameClaude, Flow: "local_cli",
		Command: "roca login " + provider.NameClaude,
	}}
}

func loginCatalogue(files ...config.File) string {
	var b strings.Builder
	b.WriteString("Supported providers:\n")
	for _, entry := range loginEntries(files...) {
		b.WriteString(fmt.Sprintf("  %-10s  %-12s  ·  %s\n",
			entry.Name, entry.humanFlow(), entry.Command))
	}
	return strings.TrimRight(b.String(), "\n")
}

// humanFlow is the terminal spelling of the machine-readable flow.
func (e loginEntry) humanFlow() string {
	switch e.Flow {
	case "local_cli":
		return "local CLI"
	}
	return e.Flow
}

func (env *cliEnv) showLoginOverview() error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return err
	}
	orderSource := "built-in default"
	if os.Getenv(provider.EnvOrder) != "" {
		orderSource = "from " + provider.EnvOrder
	} else if file.Models.Order != nil {
		orderSource = "from " + paths.Config
	}
	var order []string
	for _, p := range cascade.Providers {
		order = append(order, p.Name())
	}
	if env.json {
		return env.printJSON(map[string]any{
			"providers":      loginEntries(file),
			"authentication": "models authenticate through their own CLIs; La Roca stores no secrets",
			"configuration": map[string]any{"path": paths.Config, "order": order,
				"order_source": orderSource},
		})
	}
	env.print("%s", loginCatalogue(file))
	env.print("Model configuration:")
	env.print("  order: %s (%s · change with: models.order in %s)",
		strings.Join(order, ", "), orderSource, paths.Config)
	for _, p := range cascade.Providers {
		env.print("  %s: model %s (%s · change with: %s)",
			p.Name(), p.ModelID(), modelChoiceSource(paths.Config, p.Name(), p.ModelID()), modelChange(p.Name(), paths.Config))
	}
	env.print("Authentication: models authenticate through their own CLIs; La Roca stores no secrets and no roca login is required.")
	return nil
}

func renderModelDetection(env *cliEnv, detected, missing []string, factory bool, selected string) {
	env.print("model binaries detected: %s", detectedAgentsLine(detected))
	env.print("model binaries not found: %s", detectedAgentsLine(missing))
	if !factory {
		return
	}
	switch {
	case selected == "":
		env.print("factory default selected: none (no model provider is ready)")
	case slices.Contains(detected, selected):
		env.print("factory default selected: %s (existing local CLI session; no roca login required)", selected)
	default:
		env.print("factory default selected: %s (local runtime)", selected)
	}
}

func (env *cliEnv) loginLocalCommand(cmd *cobra.Command, paths config.Paths,
	file config.File, name, requestedModel string) error {
	model, err := env.loginModel(cmd.Context(), cmd.InOrStdin(), paths, file,
		name, requestedModel)
	if err != nil {
		return fmt.Errorf("verify the existing %s local CLI session: %w", name, err)
	}
	if env.json {
		return env.printJSON(map[string]any{
			"provider": name, "model": model,
			"model_source":              modelChoiceSource(paths.Config, name, model),
			"authentication_managed_by": "local CLI", "secrets_stored_by_roca": false,
		})
	}
	env.print("%s's local command and its existing account session are working", name)
	env.print("authentication: managed by the local CLI; La Roca stores no secrets")
	env.print("%s", modelChoiceLine(name, "selected", model, paths.Config))
	env.print("%s", loginNext(paths, name))
	return nil
}

func (env *cliEnv) loginModel(ctx context.Context, in io.Reader, paths config.Paths,
	file config.File, name, requested string) (string, error) {
	model, err := env.validatedModel(ctx, in, paths, file, name, requested)
	if err != nil {
		return "", err
	}
	order := file.Models.Order
	if len(order) == 0 {
		order = provider.DefaultOrder(nil)
	}
	order = append([]string{name}, slices.DeleteFunc(slices.Clone(order), func(current string) bool {
		return current == name
	})...)
	if err := config.SetProviderModel(paths.Config, name, model); err != nil {
		return "", err
	}
	if err := config.SetModelOrder(paths.Config, order); err != nil {
		return "", fmt.Errorf("model %s was validated and written, but update the provider order: %w", model, err)
	}
	return model, nil
}

func loginNext(paths config.Paths, name string) string {
	if !fileExists(paths.DB) {
		return fmt.Sprintf("run `roca init` next; %s will be probed before it answers", name)
	}
	return fmt.Sprintf("%s will be probed before it answers", name)
}

func modelChoiceLine(name, status, model, path string) string {
	return fmt.Sprintf("model: %s %s (%s, %s · change with: %s)",
		name, status, model, modelChoiceSource(path, name, model), modelChange(name, path))
}

func modelChange(name, path string) string {
	if slices.Contains(provider.CommandPresetNames(), name) {
		return fmt.Sprintf("roca model set <id> or models.%s.model in %s", name, path)
	}
	return fmt.Sprintf("models.%s.model in %s", name, path)
}

// modelChoiceSource names the setting that actually chose this model, so a
// setting that merely coincides with the answer is never reported as its
// source. Only Ollama reads an environment override; a shipped CLI preset
// resolves models.<name>.model, then defaults.<name>_model, then the shipped
// default, so neither an environment variable nor the loose defaults.model key
// can be what chose it.
func modelChoiceSource(path, name, model string) string {
	for _, key := range map[string][]string{
		provider.NameOllama: {"ROCA_OLLAMA_MODEL", "ROCA_MODEL"},
	}[name] {
		if os.Getenv(key) == model {
			return "from " + key
		}
	}
	preset := slices.Contains(provider.CommandPresetNames(), name)
	if file, err := config.LoadFile(path); err == nil {
		cfg := file.Models.Providers[name]
		if cfg.Model == model || file.Default(name+"_model") == model ||
			(!preset && file.Default("model") == model) {
			return "from " + path
		}
	}
	return "built-in default"
}

// modelCommand switches the answering model without touching authentication,
// which belongs entirely to the provider's own CLI.
func modelCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model",
		Short: "Change a provider's answering model without re-running login",
	}
	cmd.AddCommand(modelSetCommand(env))
	return cmd
}

func modelSetCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "set <model-id>",
		Short: "Set a provider's answering model in the configuration",
		Long: "Validates a model for the first provider in the configured order, then\n" +
			"writes models.<provider>.model in ~/.roca/config.toml and leaves every\n" +
			"other setting — the provider order and unrelated tables —\n" +
			"exactly where it was. It is the way to switch the answering model without\n" +
			"changing the provider CLI session. The former two-argument spelling remains accepted.\n\n" +
			"  roca model set gpt-5.6-sol\n" +
			"  roca model set ollama qwen3.5:4b",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				return env.modelSetCurrent(cmd.Context(), args[0])
			}
			return env.modelSetContext(cmd.Context(), args[0], args[1])
		},
	}
}

// modelSet changes one model assignment and nothing else. It does not open the
// database and it does not rewrite the order: a provider already in the cascade
// keeps its place, and only the model it answers with changes.
func (env *cliEnv) modelSet(rawName, modelID string) error {
	return env.modelSetContext(context.Background(), rawName, modelID)
}

func (env *cliEnv) modelSetContext(ctx context.Context, rawName, modelID string) error {
	name := strings.ToLower(strings.TrimSpace(rawName))
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	known := knownProviderNames(file)
	if !slices.Contains(known, name) {
		message := fmt.Sprintf("there is no provider %q\n\n%s", rawName, knownProvidersHelp(known))
		return fmt.Errorf("%s", message)
	}
	model, err := env.validatedModel(ctx, nil, paths, file, name, modelID)
	if err != nil {
		return err
	}
	if err := config.SetProviderModel(paths.Config, name, model); err != nil {
		return err
	}
	source := modelChoiceSource(paths.Config, name, model)
	if env.json {
		return env.printJSON(map[string]any{
			"provider":      name,
			"model":         model,
			"source":        source,
			"configuration": paths.Config,
		})
	}
	env.print("%s model set to %s (%s)", name, model, source)
	return nil
}

// knownProviderNames is the set a model assignment may target: the built-in
// names this build carries plus anything the operator already declared a table
// for or named in the order. A name that is none of those has no provider to set
// a model for, and the remedy names the ones that do.
func knownProviderNames(file config.File) []string {
	seen := make(map[string]bool)
	var names []string
	add := func(name string) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	add(provider.NameCodex)
	add(provider.NameOllama)
	for _, name := range provider.CommandPresetNames() {
		add(name)
	}
	for name := range file.Models.Providers {
		add(name)
	}
	for _, name := range file.Models.Order {
		add(name)
	}
	return names
}

// knownProvidersHelp is the remedy for an unknown provider: the names this build
// knows, and the command that shows the ones this configuration declared.
func knownProvidersHelp(names []string) string {
	return fmt.Sprintf(
		"Known providers: %s\n\nSee the configured providers with `roca doctor`.",
		strings.Join(names, ", "))
}
