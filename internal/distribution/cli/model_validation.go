package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"golang.org/x/term"
)

// modelsDevCacheFile is the snapshot a release before the credential strip kept
// for the HTTP Codex adapter. Nothing reads it any more; it is named here only
// so uninstall still owns and removes it on machines that have one.
const modelsDevCacheFile = "models.dev.json"

type modelCatalogue struct {
	IDs    []string
	Open   bool
	Notice string
}

type modelValidationBackend interface {
	Catalogue(context.Context, string, string) (modelCatalogue, error)
	Probe(context.Context, string, string) error
}

type modelPicker func(io.Reader, io.Writer, []string, string) (string, error)

// validatedModel is the only path from user input to a model ID a config edit
// may receive. It proves exact membership for a closed catalogue, then account
// reachability; an open local-binary catalogue accepts an explicit ID only after
// that probe. Callers persist only the returned string.
func (env *cliEnv) validatedModel(ctx context.Context, in io.Reader, paths config.Paths,
	file config.File, name, requested string) (string, error) {

	backend := env.modelBackend
	if backend == nil {
		backend = newProviderModelBackend(paths, file)
	}
	current := file.Models.Providers[name].Model
	catalogue, err := backend.Catalogue(ctx, name, current)
	if err != nil {
		return "", fmt.Errorf("read %s's model catalogue: %w; configuration was not changed", name, err)
	}
	if catalogue.Notice != "" {
		fmt.Fprintln(env.errOut, "warning:", catalogue.Notice)
	}
	if len(catalogue.IDs) == 0 {
		return "", fmt.Errorf("%s's model catalogue is empty; configuration was not changed", name)
	}

	model := strings.TrimSpace(requested)
	if model == "" {
		picker := env.modelPicker
		if picker == nil {
			if env.json || !terminalInput(in) {
				return "", fmt.Errorf("model selection needs an interactive terminal; rerun with --model <id>; configuration was not changed")
			}
			picker = terminalArrowModelPicker
		}
		model, err = picker(in, env.errOut, catalogue.IDs, current)
		if err != nil {
			return "", fmt.Errorf("choose a model: %w; configuration was not changed", err)
		}
	}
	if !catalogue.Open && !slices.Contains(catalogue.IDs, model) {
		return "", fmt.Errorf("model %q is not in %s's catalogue; configuration was not changed", model, name)
	}
	if err := backend.Probe(ctx, name, model); err != nil {
		return "", fmt.Errorf("%s model %s failed its account probe: %w; configuration was not changed", name, model, err)
	}
	return model, nil
}

func (env *cliEnv) modelSetCurrent(ctx context.Context, model string) error {
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	file, err := config.LoadFile(paths.Config)
	if err != nil {
		return err
	}
	order := file.Models.Order
	if len(order) == 0 {
		order = provider.DefaultOrder(nil)
	}
	if len(order) == 0 {
		return fmt.Errorf("there is no provider to set a model for; log in to one first")
	}
	return env.modelSetContext(ctx, order[0], model)
}

type providerModelBackend struct {
	paths config.Paths
	file  config.File
	build func(string, string) (provider.Provider, error)
}

func newProviderModelBackend(paths config.Paths, file config.File) *providerModelBackend {
	return &providerModelBackend{paths: paths, file: file}
}

// Catalogue asks the provider itself what it offers. Every provider this build
// carries answers through its own local CLI or its own local runtime, so there
// is no remote catalogue service left to consult.
func (b *providerModelBackend) Catalogue(ctx context.Context, name, current string) (modelCatalogue, error) {
	catalogueCtx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	candidate, err := b.candidate(name, current)
	if err != nil {
		return modelCatalogue{}, err
	}
	if flexible, ok := candidate.(interface{ ModelChoices() []string }); ok {
		return modelCatalogue{IDs: canonicalModelIDs(flexible.ModelChoices()), Open: true}, nil
	}
	report := candidate.Models(catalogueCtx)
	if !report.Ready {
		return modelCatalogue{}, fmt.Errorf("%s", report.Reason)
	}
	return modelCatalogue{IDs: canonicalModelIDs(report.Models)}, nil
}

func (b *providerModelBackend) Probe(ctx context.Context, name, model string) error {
	candidate, err := b.candidate(name, model)
	if err != nil {
		return err
	}
	timeout := b.timeout()
	if timed, ok := candidate.(interface{ RequestTimeout() time.Duration }); ok &&
		b.file.Models.ProbeMS <= 0 {
		timeout = timed.RequestTimeout()
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return provider.ProbeModel(probeCtx, candidate)
}

func (b *providerModelBackend) timeout() time.Duration {
	if b.file.Models.ProbeMS > 0 {
		return time.Duration(b.file.Models.ProbeMS) * time.Millisecond
	}
	return provider.ProbeTimeout
}

func (b *providerModelBackend) candidate(name, model string) (provider.Provider, error) {
	if b.build != nil {
		return b.build(name, model)
	}
	file := b.file
	file.Models.Providers = cloneProviderConfigs(file.Models.Providers)
	providerConfig := file.Models.Providers[name]
	providerConfig.Model = model
	providerConfig.Values = cloneStrings(providerConfig.Values)
	providerConfig.Values["model"] = model
	file.Models.Providers[name] = providerConfig
	file.Models.Order = []string{name}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, RunnerDir: b.paths.Runner,
		Env: validationEnvironment,
	})
	if err != nil {
		return nil, err
	}
	if len(cascade.Providers) != 1 {
		return nil, fmt.Errorf("provider %s cannot be built for validation", name)
	}
	return cascade.Providers[0], nil
}

func cloneProviderConfigs(source map[string]config.ProviderConfig) map[string]config.ProviderConfig {
	clone := make(map[string]config.ProviderConfig, len(source)+1)
	for name, value := range source {
		value.Command = slices.Clone(value.Command)
		value.Values = cloneStrings(value.Values)
		clone[name] = value
	}
	return clone
}

func cloneStrings(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func validationEnvironment(key string) string {
	switch key {
	case provider.EnvOrder, "ROCA_CODEX_MODEL", "ROCA_OLLAMA_MODEL", "ROCA_MODEL":
		return ""
	default:
		return os.Getenv(key)
	}
}

func canonicalModelIDs(models []string) []string {
	seen := make(map[string]bool, len(models))
	canonical := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model != "" && !seen[model] {
			seen[model] = true
			canonical = append(canonical, model)
		}
	}
	sort.Strings(canonical)
	return canonical
}

func terminalArrowModelPicker(in io.Reader, out io.Writer, models []string, current string) (string, error) {
	file, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("model picker needs an interactive terminal")
	}
	state, err := term.MakeRaw(int(file.Fd()))
	if err != nil {
		return "", err
	}
	defer term.Restore(int(file.Fd()), state)
	return readArrowModelChoice(file, out, models, current)
}

func readArrowModelChoice(in io.Reader, out io.Writer, models []string, current string) (string, error) {
	if len(models) == 0 {
		return "", fmt.Errorf("there are no models to choose from")
	}
	selected := slices.Index(models, current)
	if selected < 0 {
		selected = 0
	}
	reader := bufio.NewReader(in)
	firstDraw := true
	for {
		if !firstDraw {
			fmt.Fprintf(out, "\x1b[%dA", len(models)+1)
		}
		firstDraw = false
		fmt.Fprint(out, "Choose a model. Use ↑/↓ and Enter:\r\n")
		for index, model := range models {
			marker := "  "
			if index == selected {
				marker = "> "
			}
			fmt.Fprintf(out, "\x1b[2K%s%s\r\n", marker, model)
		}

		key, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		switch key {
		case '\r', '\n':
			return models[selected], nil
		case 3:
			return "", fmt.Errorf("selection canceled")
		case 0x1b:
			open, openErr := reader.ReadByte()
			direction, directionErr := reader.ReadByte()
			if openErr != nil || directionErr != nil || open != '[' {
				continue
			}
			switch direction {
			case 'A':
				selected = (selected - 1 + len(models)) % len(models)
			case 'B':
				selected = (selected + 1) % len(models)
			}
		}
	}
}
