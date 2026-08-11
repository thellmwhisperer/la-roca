package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/oauth"
	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"golang.org/x/term"
)

func doctorCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the configuration and which model is going to answer",
		Long: "Reports where the data and the configuration are, which providers this\n" +
			"installation declares, which of them are available and, for the ones that\n" +
			"are not, the exact command that fixes it. It reports that a credential is\n" +
			"present, never its value.",
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
			if env.json {
				return env.printJSON(report)
			}
			renderDoctor(env, report)
			if terminalInput(cmd.InOrStdin()) && !env.skipReconciliation {
				_, err = env.reconcileCapabilities(cmd, true, true)
			}
			return err
		}),
	}
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
			status = map[string]string{service.CredentialPresent: "present-but-failed",
				service.CredentialAbsent: "absent"}[p.Credential]
			if status == "" {
				status = "failed"
			}
		}
		env.print("  %s %s · model %s (%s · change with: %s) · credential %s · probe %s",
			env.mark(p.Ready), p.Name, orDash(p.Model), modelChoiceSource(configPath, p.Name, p.Model),
			modelChange(p.Name, configPath), p.Credential, status)
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

const modelsHelp = "" +
	"List the models each configured provider offers, and mark the one that is\n" +
	"going to answer.\n" +
	"\n" +
	"  roca models           # every provider in the declared order\n" +
	"\n" +
	"A provider is asked for its catalogue (its /models endpoint for a key\n" +
	"provider, /api/tags for Ollama). One that does not answer is shown as\n" +
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
		File: file, Credentials: paths.Credentials, RunnerDir: paths.Runner, Env: os.Getenv,
	})
	if err != nil {
		return nil, nil, err
	}
	return cascade.Models(cmd.Context()), cascade.Warnings, nil
}

// renderModels is the readable form of the catalogue. The provider header names
// the model the cascade would use (Selected); the list beneath it shows what the
// credential or the local runtime reaches, with that model marked. A provider
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
	"Log in to a model provider. Same verb for every provider this build ships:\n" +
	"\n" +
	"  subscription flow  roca login codex\n" +
	"  local CLI session  roca login claude\n" +
	"  API key            roca login xai\n" +
	"  API key            roca login zai\n" +
	"  API key            roca login deepseek\n" +
	"\n" +
	"A local CLI login verifies the binary and its existing vendor session. La Roca\n" +
	"never reads or stores that credential.\n" +
	"\n" +
	"A subscription login opens the vendor's browser flow and leaves the session\n" +
	"on this machine, readable only by you. It renews itself: you log in once.\n" +
	"\n" +
	"A key login prompts for the API key (no echo when the terminal supports it),\n" +
	"stores it under the credentials directory at 0600, and never prints it back.\n" +
	"Config-file api_key and the provider's environment variable keep working;\n" +
	"a key stored by login takes precedence.\n" +
	"\n" +
	"After login, choose a model with the arrow keys. The choice is checked against\n" +
	"the provider catalogue and probed with the new credential before config changes.\n" +
	"Scripts can use --model <id>; the ID is subject to the same checks before it\n" +
	"is written under [models.<provider>] in ~/.roca/config.toml.\n\n" +
	"With no provider argument, lists what this build supports."

func loginCommand(env *cliEnv) *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "login [provider]",
		Short: "Log in to a model provider",
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
			if provider.UsesCommandTransport(file, name) {
				return env.loginLocalCommand(cmd, paths, file, name, model)
			}
			switch {
			case name == provider.NameCodex:
				return env.loginCodex(cmd, model)
			case provider.IsKeyProvider(name):
				return env.loginKey(cmd, name, model)
			default:
				return fmt.Errorf(
					"there is no login for %q\n\n%s",
					args[0], loginCatalogue())
			}
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

// loginEntries is the single source of what this build can log in to: the
// subscription and local-session flows first, then every key provider.
func loginEntries() []loginEntry {
	entries := []loginEntry{{
		Name: provider.NameCodex, Flow: "subscription",
		Command: "roca login " + provider.NameCodex,
	}, {
		Name: provider.NameClaude, Flow: "local_cli",
		Command: "roca login " + provider.NameClaude,
	}}
	for _, name := range provider.KeyProviders() {
		entries = append(entries, loginEntry{
			Name: name, Flow: "api_key", Command: "roca login " + name,
		})
	}
	return entries
}

func loginCatalogue() string {
	var b strings.Builder
	b.WriteString("Supported providers:\n")
	for _, entry := range loginEntries() {
		b.WriteString(fmt.Sprintf("  %-10s  %-12s  ·  %s\n",
			entry.Name, entry.humanFlow(), entry.Command))
	}
	return strings.TrimRight(b.String(), "\n")
}

// humanFlow is the terminal spelling of the machine-readable flow.
func (e loginEntry) humanFlow() string {
	switch e.Flow {
	case "subscription":
		return "subscription"
	case "local_cli":
		return "local CLI"
	}
	return "API key"
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
		File: file, Credentials: paths.Credentials, RunnerDir: paths.Runner, Env: os.Getenv,
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
	store := provider.CodexStore(paths.Credentials)
	codexState := "no session"
	if token, loadErr := store.Load(); loadErr == nil {
		codexState = "session present (no expiry reported)"
		if !token.ExpiresAt.IsZero() {
			codexState = "session present (expires " + token.ExpiresAt.UTC().Format("2006-01-02") + ")"
		}
	} else if !os.IsNotExist(loadErr) {
		codexState = "session present but unreadable"
	}
	states := map[string]string{provider.NameCodex: codexState}
	states[provider.NameClaude] = "existing Claude Code session; La Roca stores no credential"
	for _, name := range provider.KeyProviders() {
		states[name] = "no stored API key"
		if fileExists(provider.APIKeyPath(paths.Credentials, name)) {
			states[name] = "stored API key present"
		}
	}
	states[provider.NameOllama] = "local runtime, no credential needed"
	if env.json {
		return env.printJSON(map[string]any{
			"providers": loginEntries(), "credentials": states,
			"configuration": map[string]any{"path": paths.Config, "order": order,
				"order_source": orderSource},
		})
	}
	env.print("%s", loginCatalogue())
	env.print("Model configuration:")
	env.print("  order: %s (%s · change with: models.order in %s)",
		strings.Join(order, ", "), orderSource, paths.Config)
	for _, p := range cascade.Providers {
		env.print("  %s: model %s (%s · change with: %s)",
			p.Name(), p.ModelID(), modelChoiceSource(paths.Config, p.Name(), p.ModelID()), modelChange(p.Name(), paths.Config))
	}
	env.print("Credential and session state:")
	for _, entry := range loginEntries() {
		env.print("  %s: %s", entry.Name, states[entry.Name])
	}
	env.print("  %s: %s", provider.NameOllama, states[provider.NameOllama])
	return nil
}

func (env *cliEnv) loginCodex(cmd *cobra.Command, requestedModel string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	store := provider.CodexStore(paths.Credentials)
	// The narrative (the address to open, the waiting line) is prompt output,
	// so it travels on stderr: under --json stdout has to carry only the result
	// envelope, and prompts on stdout are data a program cannot parse.
	token, err := provider.CodexFlow().Login(cmd.Context(), oauth.LoginOptions{
		Out: env.errOut,
	})
	if err != nil {
		return err
	}
	if err := store.Save(token); err != nil {
		return err
	}
	model, err := env.loginModel(cmd.Context(), cmd.InOrStdin(), paths, file,
		provider.NameCodex, requestedModel)
	if err != nil {
		return fmt.Errorf("%w; the %s session is stored at %s, so rerun `roca model set <id>` to finish",
			err, provider.NameCodex, store.Path)
	}

	if env.json {
		return env.printJSON(map[string]any{
			"provider":     provider.NameCodex,
			"session":      store.Path,
			"expires_at":   token.ExpiresAt,
			"account_id":   token.AccountID,
			"model":        model,
			"model_source": modelChoiceSource(paths.Config, provider.NameCodex, model),
		})
	}
	if token.AccountID != "" {
		env.print("signed in to %s as account %s", provider.NameCodex, token.AccountID)
	} else {
		env.print("signed in to %s", provider.NameCodex)
	}
	env.print("the session is at %s and renews itself", store.Path)
	env.print("%s", modelChoiceLine(provider.NameCodex, "selected", model, paths.Config))
	env.print("%s", loginNext(paths, provider.NameCodex))
	env.print("revoke it with `roca logout %s`", provider.NameCodex)
	return nil
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
			"model_source":          modelChoiceSource(paths.Config, name, model),
			"credential_managed_by": "local CLI", "credential_seen_by_roca": false,
		})
	}
	env.print("%s's local command and its existing account session are working", name)
	env.print("credential: managed by the local CLI; La Roca never reads or stores it")
	env.print("%s", modelChoiceLine(name, "selected", model, paths.Config))
	env.print("%s", loginNext(paths, name))
	return nil
}

func (env *cliEnv) loginKey(cmd *cobra.Command, name, requestedModel string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	preset, _ := provider.Preset(name)
	label := preset.Label
	if label == "" {
		label = name
	}
	// The prompt is prompt output, not data: it goes to stderr so stdout stays a
	// clean result envelope under --json.
	key, err := readSecret(fmt.Sprintf("Paste your %s API key: ", label), cmd.InOrStdin(), env.errOut)
	if err != nil {
		return err
	}
	if err := provider.SaveAPIKey(paths.Credentials, name, key); err != nil {
		return err
	}
	path := provider.APIKeyPath(paths.Credentials, name)
	model, err := env.loginModel(cmd.Context(), cmd.InOrStdin(), paths, file, name, requestedModel)
	if err != nil {
		return fmt.Errorf("%w; the %s credential is stored at %s, so rerun `roca model set <id>` to finish",
			err, name, path)
	}
	if env.json {
		return env.printJSON(map[string]any{
			"provider":     name,
			"path":         path,
			"model":        model,
			"model_source": modelChoiceSource(paths.Config, name, model),
		})
	}
	env.print("logged in to %s: the key is at %s", name, path)
	env.print("%s", modelChoiceLine(name, "selected", model, paths.Config))
	env.print("%s", loginNext(paths, name))
	env.print("forget it with `roca logout %s`", name)
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
		order = provider.DefaultOrder()
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
	if name == provider.NameOllama || (!provider.IsKeyProvider(name) && name != provider.NameCodex) {
		return fmt.Sprintf("models.%s.model in %s", name, path)
	}
	return fmt.Sprintf("roca login %s --model <id> or models.%s.model in %s", name, name, path)
}

func modelChoiceSource(path, name, model string) string {
	for _, key := range map[string][]string{
		provider.NameCodex: {"ROCA_CODEX_MODEL"}, provider.NameOllama: {"ROCA_OLLAMA_MODEL", "ROCA_MODEL"},
	}[name] {
		if os.Getenv(key) == model {
			return "from " + key
		}
	}
	if file, err := config.LoadFile(path); err == nil {
		cfg := file.Models.Providers[name]
		if cfg.Model == model || file.Default(name+"_model") == model ||
			(name != provider.NameCodex && file.Default("model") == model) {
			return "from " + path
		}
	}
	return "built-in default"
}

func readSecret(prompt string, in io.Reader, out io.Writer) (string, error) {
	fmt.Fprint(out, prompt)
	if file, ok := in.(*os.File); ok {
		fd := int(file.Fd())
		if term.IsTerminal(fd) {
			raw, err := term.ReadPassword(fd)
			fmt.Fprintln(out)
			if err != nil {
				return "", err
			}
			key := strings.TrimSpace(string(raw))
			if key == "" {
				return "", fmt.Errorf("the API key is empty")
			}
			return key, nil
		}
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", err
	}
	key := strings.TrimSpace(line)
	if key == "" {
		return "", fmt.Errorf("the API key is empty")
	}
	return key, nil
}

func logoutCommand(env *cliEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "logout <provider>",
		Short: "Forget a provider's credential",
		Long: "Forgets a subscription session or a stored API key. Forgetting what\n" +
			"was already forgotten is not a failure: the end state is the one asked for.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			switch {
			case name == provider.NameCodex:
				store, err := env.codexStore()
				if err != nil {
					return err
				}
				if err := store.Delete(); err != nil {
					return err
				}
				return env.forget(name, "session")
			case provider.IsKeyProvider(name):
				paths, err := env.resolvePaths()
				if err != nil {
					return err
				}
				if err := provider.DeleteAPIKey(paths.Credentials, name); err != nil {
					return err
				}
				return env.forget(name, "credential")
			default:
				return fmt.Errorf(
					"there is no credential store for %q. Supported providers:\n%s",
					args[0], loginCatalogue())
			}
		},
	}
}

func (env *cliEnv) codexStore() (oauth.Store, error) {
	paths, err := env.resolvePaths()
	if err != nil {
		return oauth.Store{}, err
	}
	return provider.CodexStore(paths.Credentials), nil
}

// forget is the one confirmation a logout prints: a document under --json
// (provider, forgotten) and the same "<what> for <provider> forgotten" line a
// human reads otherwise. The codex session and a stored key are different words
// for the same end state.
func (env *cliEnv) forget(name, what string) error {
	if env.json {
		return env.printJSON(map[string]any{"provider": name, "forgotten": true})
	}
	env.print("%s for %s forgotten", what, name)
	return nil
}

// modelCommand is the no-login way to switch the answering model. It changes a
// model assignment in the configuration without touching a credential: the
// operator who already logged in picks a different model the same provider
// serves, and re-running OAuth or re-entering a key to do that is the cost
// `roca model set` exists to remove.
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
			"other setting — the provider order, the credentials, unrelated tables —\n" +
			"exactly where it was. It is the way to switch the answering model without\n" +
			"re-running a login flow. The former two-argument spelling remains accepted.\n\n" +
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
	for _, name := range provider.PresetNames() {
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
