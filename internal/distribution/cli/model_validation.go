package cli

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
	"golang.org/x/term"
)

const (
	modelsDevURL       = "https://models.dev/api.json"
	modelsDevCacheFile = "models.dev.json"
	catalogueTimeout   = 5 * time.Second
)

//go:embed models_dev_snapshot.json
var embeddedModelsDevSnapshot []byte

type modelCatalogue struct {
	IDs    []string
	Stale  bool
	Open   bool
	Notice string
}

type modelValidationBackend interface {
	Catalogue(context.Context, string, string) (modelCatalogue, error)
	Probe(context.Context, string, string) error
}

type modelPicker func(io.Reader, io.Writer, []string, string) (string, error)

type modelCatalogRefresher func(context.Context) error

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
	paths      config.Paths
	file       config.File
	client     *http.Client
	catalogURL string
	build      func(string, string) (provider.Provider, error)
}

func newProviderModelBackend(paths config.Paths, file config.File) *providerModelBackend {
	return &providerModelBackend{paths: paths, file: file, client: http.DefaultClient, catalogURL: modelsDevURL}
}

func (b *providerModelBackend) Catalogue(ctx context.Context, name, current string) (modelCatalogue, error) {
	catalogueCtx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()
	if name == provider.NameCodex && !provider.UsesCommandTransport(b.file, name) {
		return readCodexCatalogue(catalogueCtx, b.client, b.catalogURL, modelsDevCachePath(b.paths))
	}
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
		File: file, Credentials: b.paths.Credentials, RunnerDir: b.paths.Runner,
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

type cachedModelSnapshot struct {
	RefreshedAt string   `json:"refreshed_at"`
	Models      []string `json:"models"`
}

func modelsDevCachePath(paths config.Paths) string {
	return filepath.Join(filepath.Dir(paths.DB), "cache", modelsDevCacheFile)
}

func readCodexCatalogue(ctx context.Context, client *http.Client, url, cachePath string) (modelCatalogue, error) {
	models, err := fetchModelsDev(ctx, client, url)
	if err == nil {
		_ = writeModelSnapshot(cachePath, models)
		return modelCatalogue{IDs: models}, nil
	}
	reason := err.Error()
	if cached, cacheErr := readModelSnapshotFile(cachePath); cacheErr == nil {
		return modelCatalogue{IDs: cached, Stale: true, Notice: fmt.Sprintf(
			"the live Codex catalogue could not be read (%s); using the cached snapshot, which is possibly stale", reason)}, nil
	}
	embedded, embeddedErr := parseModelSnapshot(embeddedModelsDevSnapshot)
	if embeddedErr != nil {
		return modelCatalogue{}, fmt.Errorf("live catalogue: %v; embedded snapshot: %w", err, embeddedErr)
	}
	return modelCatalogue{IDs: embedded, Stale: true, Notice: fmt.Sprintf(
		"the live Codex catalogue could not be read (%s); using the embedded snapshot, which is possibly stale", reason)}, nil
}

func refreshCodexCatalogue(ctx context.Context, client *http.Client, url, cachePath string) error {
	models, err := fetchModelsDev(ctx, client, url)
	if err != nil {
		return err
	}
	return writeModelSnapshot(cachePath, models)
}

func (env *cliEnv) refreshModelCatalogue(ctx context.Context) error {
	if env.modelCatalogRefresh != nil {
		return env.modelCatalogRefresh(ctx)
	}
	paths, err := env.resolvePaths()
	if err != nil {
		return err
	}
	return refreshCodexCatalogue(ctx, http.DefaultClient, modelsDevURL, modelsDevCachePath(paths))
}

func fetchModelsDev(ctx context.Context, client *http.Client, url string) ([]string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, catalogueTimeout)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("models.dev answered %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var document map[string]struct {
		Models map[string]struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			Modalities struct {
				Output []string `json:"output"`
			} `json:"modalities"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&document); err != nil {
		return nil, fmt.Errorf("decode models.dev: %w", err)
	}
	openAI, ok := document["openai"]
	if !ok {
		return nil, fmt.Errorf("models.dev lists no openai provider")
	}
	models := make([]string, 0, len(openAI.Models))
	for key, entry := range openAI.Models {
		id := entry.ID
		if id == "" {
			id = key
		}
		if entry.Status != "deprecated" && slices.Contains(entry.Modalities.Output, "text") {
			models = append(models, id)
		}
	}
	models = canonicalModelIDs(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("models.dev lists no active OpenAI text models")
	}
	return models, nil
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

func writeModelSnapshot(path string, models []string) error {
	payload, err := json.MarshalIndent(cachedModelSnapshot{
		RefreshedAt: time.Now().UTC().Format(time.RFC3339), Models: canonicalModelIDs(models),
	}, "", "  ")
	if err != nil {
		return err
	}
	return securefile.Write(path, append(payload, '\n'), 0o600, 0o700)
}

func readModelSnapshotFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseModelSnapshot(raw)
}

func parseModelSnapshot(raw []byte) ([]string, error) {
	var snapshot cachedModelSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	models := canonicalModelIDs(snapshot.Models)
	if len(models) == 0 {
		return nil, fmt.Errorf("model snapshot is empty")
	}
	return models, nil
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
