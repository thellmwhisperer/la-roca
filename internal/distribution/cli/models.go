package cli

import (
	"bufio"
	"context"
	"errors"
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
			audit := logfile.New(svc.DataDir())
			if env.auditOpsDatabase != "" {
				audit = logfile.NewWithOps(svc.DataDir(), env.auditOpsDatabase)
			}
			failures, logErr := audit.RecentQueryFailures(
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
	if len(report.LayerRepairs) > 0 {
		env.print("runtime_layers_not_in_registry: failed")
		for _, command := range report.LayerRepairs {
			env.print("      remedy: run `%s`", command)
		}
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
	renderExploration(env, report)
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

func renderExploration(env *cliEnv, report service.DoctorReport) {
	if len(report.Explorers) == 0 {
		return
	}
	env.print("deep exploration providers, in the declared order:")
	renderProviders(env, report.ConfigPath, report.Explorers)
	if report.ExploreTitular != "" {
		env.print("the one that is going to read deep exploration rows: %s", report.ExploreTitular)
		return
	}
	env.print("no deep exploration provider is available: deep mode falls back to " +
		"interpretation order, then main order")
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
			cascade, listings, err := env.resolveModels(cmd)
			if err != nil {
				return err
			}
			if env.json {
				return env.printJSON(map[string]any{
					"version":    env.build.Version,
					"source_sha": env.build.Commit,
					"providers":  listings,
					"reason":     emptyCascadeText(cascade, listings),
					"warnings":   orNoWarnings(cascade.Warnings),
				})
			}
			renderModels(env, cascade, listings)
			return nil
		},
	}
}

// resolveModels builds the cascade from the configuration alone and asks every
// provider for its catalogue. It is a question about providers, not memory, so
// it never opens the database and runs before init.
func (env *cliEnv) resolveModels(cmd *cobra.Command) (provider.Cascade, []provider.ModelsListing, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return provider.Cascade{}, nil, err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return provider.Cascade{}, nil, err
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return provider.Cascade{}, nil, err
	}
	return cascade, cascade.Models(cmd.Context()), nil
}

// renderModels is the readable form of the catalogue. The provider header names
// the model the cascade would use (Selected); the list beneath it shows what the
// command transport or the local runtime reaches, with that model marked. A provider
// that could not be reached shows its reason instead of a list. An empty catalogue
// names which empty cascade it is, the same distinction `model check` makes.
func renderModels(env *cliEnv, cascade provider.Cascade, listings []provider.ModelsListing) {
	env.printCascadeWarnings(cascade.Warnings)
	if len(listings) == 0 {
		env.print("%s", emptyCascadeReason(cascade))
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

func loginCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "login [provider]",
		Short: "Compatibility alias for model check",
		Long: "Compatibility alias for `roca model check`. It probes the configured\n" +
			"model through the provider's existing CLI session and never writes configuration.\n" +
			"Models authenticate through their own CLIs; La Roca stores no secrets.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.modelCheck(cmd.Context(), args)
		},
	}
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
		env.print("factory default selected: %s (existing local CLI session; confirm it with roca model check %s)",
			selected, selected)
	default:
		env.print("factory default selected: %s (local runtime)", selected)
	}
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
		Short: "Check or set a provider's answering model",
	}
	cmd.AddCommand(modelCheckCommand(env), modelSetCommand(env))
	return cmd
}

func modelCheckCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "check [provider]",
		Short: "Probe whether a provider's configured model answers",
		Long: "Probes the configured model through the provider's existing CLI session.\n" +
			"Models authenticate through their own CLIs; La Roca stores no secrets.\n" +
			"The probe never writes configuration or changes provider order.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return env.modelCheck(cmd.Context(), args)
		},
	}
}

func (env *cliEnv) modelCheck(ctx context.Context, args []string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	name, model, warnings, err := effectiveModelTarget(paths, file, args)
	if errors.Is(err, errNoProviderDeclared) || errors.Is(err, errNoProviderUsable) {
		return env.reportNothingToCheck(err, warnings)
	}
	if err != nil {
		return err
	}
	if !env.json {
		env.printCascadeWarnings(warnings)
	}
	backend := env.modelBackend
	if backend == nil {
		backend = newProviderModelBackend(paths, file)
	}
	if err := backend.Probe(ctx, name, model); err != nil {
		return fmt.Errorf("%s model %s failed its account probe: %w; configuration was not changed", name, model, err)
	}
	if env.json {
		return env.printJSON(map[string]any{
			"provider": name, "model": model, "ready": true, "reason": "",
			"warnings": orNoWarnings(warnings), "configuration_changed": false,
		})
	}
	env.print("%s model %s answered the probe; configuration was not changed", name, model)
	env.print("authentication: %s answers through its own CLI session; La Roca stores no secrets", name)
	return nil
}

// The empty cascade has two causes and they are not the same answer. One is the
// operator's own decision: nothing declared, or the order turned off. The other
// is this build dropping every provider the configuration named, which is a
// state the warnings explain and which reporting as "nothing was declared"
// would deny.
var (
	errNoProviderDeclared = errors.New("no provider is declared")
	errNoProviderUsable   = errors.New("no declared provider can be used by this build")
)

// reportNothingToCheck answers an empty cascade the way `roca models` does,
// with the answer itself rather than an error: there is no session to probe.
func (env *cliEnv) reportNothingToCheck(reason error, warnings []string) error {
	if env.json {
		return env.printJSON(map[string]any{
			"provider": "", "model": "", "ready": false,
			"reason": reason.Error(), "warnings": orNoWarnings(warnings),
			"configuration_changed": false,
		})
	}
	env.printCascadeWarnings(warnings)
	env.print("%s, so there is no model to probe; configuration was not changed", reason.Error())
	return nil
}

func (env *cliEnv) printCascadeWarnings(warnings []string) {
	for _, warning := range warnings {
		env.print("warning: %s", warning)
	}
}

// orNoWarnings keeps the machine field a list in every answer, so a reader that
// ranges over it never has to tell an absent key from an empty one.
func orNoWarnings(warnings []string) []string {
	if warnings == nil {
		return []string{}
	}
	return warnings
}

func effectiveModelTarget(paths config.Paths, file config.File, args []string) (string, string, []string, error) {
	if len(args) == 0 {
		return firstCascadeProvider(paths, file)
	}
	name := strings.ToLower(strings.TrimSpace(args[0]))
	known := knownProviderNames(file)
	if !slices.Contains(known, name) {
		return "", "", nil, fmt.Errorf("there is no provider %q\n\n%s", args[0], knownProvidersHelp(known))
	}
	model, warnings, err := effectiveProviderModel(paths, file, name)
	return name, model, warnings, err
}

// firstCascadeProvider is the provider a model command acts on when the operator
// names none. `model check` and `model set` both resolve it here, through the
// live cascade rather than the raw models.order key, so the two never disagree
// about which provider is first when the environment sets the order.
func firstCascadeProvider(paths config.Paths, file config.File) (string, string, []string, error) {
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return "", "", nil, err
	}
	if len(cascade.Providers) == 0 {
		return "", "", cascade.Warnings, emptyCascadeReason(cascade)
	}
	return cascade.Providers[0].Name(), cascade.Providers[0].ModelID(), cascade.Warnings, nil
}

// emptyCascadeReason tells the two empty cascades apart from what the cascade
// itself dropped, never from its warnings: those also carry retired keys and
// unknown keys of providers the order never named, so a configuration that
// declared nothing but wrote one stale key would be reported as an order this
// build refused.
func emptyCascadeReason(cascade provider.Cascade) error {
	if cascade.Disabled || len(cascade.Dropped) == 0 {
		return errNoProviderDeclared
	}
	return errNoProviderUsable
}

// emptyCascadeText is the machine form of that same distinction, and it is
// empty exactly when there was a catalogue to report. A reader of `roca models
// --json` sees an empty provider list for both causes, and the warning list is
// what cannot tell them apart, so the reason has to travel as its own field.
func emptyCascadeText(cascade provider.Cascade, listings []provider.ModelsListing) string {
	if len(listings) > 0 {
		return ""
	}
	return emptyCascadeReason(cascade).Error()
}

func effectiveProviderModel(paths config.Paths, file config.File, name string) (string, []string, error) {
	file.Models.Order = []string{name}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: paths.Runner,
		Env: func(key string) string {
			if key == provider.EnvOrder {
				return ""
			}
			return os.Getenv(key)
		},
	})
	if err != nil {
		return "", nil, err
	}
	for _, candidate := range cascade.Providers {
		if candidate.Name() == name {
			return candidate.ModelID(), cascade.Warnings, nil
		}
	}
	return "", cascade.Warnings, fmt.Errorf("provider %s cannot be built for a model check%s",
		name, warningDetail(cascade.Warnings))
}

// warningDetail appends the cascade's own account of what it dropped to a
// failure that would otherwise name only the provider. Every drop is narrated in
// a warning; a bare "cannot be built" hides the one thing the operator can act on.
func warningDetail(warnings []string) string {
	if len(warnings) == 0 {
		return ""
	}
	return ": " + strings.Join(warnings, "; ")
}

func modelSetCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "set [model-id] | <provider> [model-id]",
		Short: "Set a provider's answering model in the configuration",
		Long: "Validates a model for the first provider in the configured order, then\n" +
			"writes models.<provider>.model in ~/.roca/config.toml and leaves every\n" +
			"other setting — the provider order and unrelated tables —\n" +
			"exactly where it was. It is the way to switch the answering model without\n" +
			"changing the provider CLI session. With no ID, an interactive terminal chooses\n" +
			"from the first provider's catalogue; name a provider to choose from its catalogue.\n\n" +
			"  roca model set gpt-5.6-luna\n" +
			"  roca model set claude\n" +
			"  roca model set ollama qwen3.5:4b",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch len(args) {
			case 0:
				return env.modelSetCurrentInput(cmd.Context(), cmd.InOrStdin(), "")
			case 1:
				return env.modelSetArgument(cmd.Context(), cmd.InOrStdin(), args[0])
			default:
				return env.modelSetContext(cmd.Context(), cmd.InOrStdin(), args[0], args[1])
			}
		},
	}
}

func (env *cliEnv) modelSetArgument(ctx context.Context, in io.Reader, argument string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(argument))
	if slices.Contains(knownProviderNames(file), name) {
		return env.modelSetContext(ctx, in, name, "")
	}
	return env.modelSetCurrentInput(ctx, in, argument)
}

func (env *cliEnv) modelSetContext(ctx context.Context, in io.Reader, rawName, modelID string) error {
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
	model, err := env.validatedModel(ctx, in, paths, file, name, modelID)
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
