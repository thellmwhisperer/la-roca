package provider

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

// EnvOrder is the provider order asked for out loud, right now. It wins over
// the file and is treated as a contract.
const EnvOrder = "ROCA_MODELS_ORDER"

var errRetiredTransport = errors.New("its retired authentication transport is still configured and is ignored; accept or decline the migration proposal before using this provider")

// Settings is everything needed to turn a configuration into a live cascade.
type Settings struct {
	File      config.File
	RunnerDir string
	Env       func(key string) string
	LookPath  LookPathFunc
}

func (s Settings) env(keys ...string) string {
	if s.Env == nil {
		return ""
	}
	for _, key := range keys {
		if value := s.Env(key); value != "" {
			return value
		}
	}
	return ""
}

// BuildCascade turns the configuration into the resolved provider order.
func BuildCascade(s Settings) (Cascade, error) {
	catalog := s.catalog()
	detected := DetectedCommandPresets(s.LookPath)
	selection := s.selection(catalog, detected)
	resolved, err := Resolve(selection, catalog)
	if err != nil {
		return Cascade{}, err
	}
	cascade := Cascade{
		Providers:           resolved.Providers,
		DetectedBinaries:    detected,
		FallbackDiagnostics: missingBinaryDiagnostics(detected),
		Disabled:            resolved.Disabled,
		Warnings:            append(append([]string(nil), s.File.Warnings...), resolved.Warnings...),
		FactoryDefault:      selection.Source == SourceCode,
	}
	return s.budgeted(cascade), nil
}

// BuildInterpretCascade turns models.interpret_order into the cascade of the
// second inference, the only one that sees result rows.
func BuildInterpretCascade(s Settings) (Cascade, error) {
	order := s.File.Models.InterpretOrder
	if len(order) == 0 {
		return Cascade{}, nil
	}
	resolved, err := Resolve(Selection{
		Names: order, Source: SourceConfig, File: s.File.Path, Key: "models.interpret_order",
	}, s.catalog())
	if err != nil {
		return Cascade{}, err
	}
	return s.budgeted(Cascade{Providers: resolved.Providers, Warnings: resolved.Warnings}), nil
}

func (s Settings) budgeted(cascade Cascade) Cascade {
	if ms := s.File.Models.TimeoutMS; ms > 0 {
		cascade.Timeout = time.Duration(ms) * time.Millisecond
	}
	if ms := s.File.Models.ProbeMS; ms > 0 {
		cascade.Probe = time.Duration(ms) * time.Millisecond
	}
	return cascade
}

func (s Settings) selection(catalog Catalog, detected []string) Selection {
	if raw := s.env(EnvOrder); raw != "" {
		return Selection{Names: splitList(raw), Source: SourceEnv, Key: EnvOrder}
	}
	if order := s.File.Models.Order; order != nil {
		return Selection{Names: order, Source: SourceConfig, File: s.File.Path, Key: "models.order"}
	}
	return Selection{Names: presentIn(defaultOrderFromDetected(detected), catalog), Source: SourceCode}
}

func missingBinaryDiagnostics(detected []string) []Attempt {
	var missing []Attempt
	for _, name := range MissingCommandPresets(detected) {
		preset := commandPresets[name]
		missing = append(missing, Attempt{Name: name, ModelID: preset.Model,
			Reason: filepath.Base(preset.Command[0]) + " binary not found in PATH", Action: preset.Action})
	}
	return missing
}

// catalog contains only local command transports and Ollama. A provider table
// can add another command, but never an HTTP or credential-backed adapter.
func (s Settings) catalog() Catalog {
	catalog := Catalog{
		NameOllama: s.withCommand(NameOllama, func() (Provider, error) { return s.ollama(), nil }),
	}
	for name, preset := range commandPresets {
		catalog[name] = s.binaryPresetFactory(name, preset)
	}
	for name, cfg := range s.File.Models.Providers {
		if _, exists := catalog[name]; !exists && len(cfg.Command) > 0 {
			catalog[name] = s.withCommand(name, nil)
		}
	}
	return catalog
}

func (s Settings) binaryPresetFactory(name string, preset CommandPreset) Factory {
	return func() (Provider, error) {
		cfg := s.File.Models.Providers[name]
		if retiredProviderConfig(name, cfg) {
			return nil, errRetiredTransport
		}
		command, action, responseFormat := cfg.Command, "", cfg.ResponseFormat
		if len(command) == 0 {
			command, action = preset.Command, preset.Action
			if responseFormat == "" {
				responseFormat = preset.ResponseFormat
			}
		}
		return s.localBinary(name, command, firstNonEmpty(
			cfg.Model, s.File.Default(name+"_model"), preset.Model), preset.Models,
			cfg.Values, firstNonZero(cfg.TimeoutSeconds, preset.TimeoutSeconds), action, responseFormat)
	}
}

// UsesCommandTransport reports whether this build can run the provider without
// owning authentication. Legacy HTTP fields never override a shipped CLI.
func UsesCommandTransport(file config.File, name string) bool {
	name = normalize(name)
	if len(file.Models.Providers[name].Command) > 0 {
		return true
	}
	_, preset := commandPresets[name]
	return preset
}

func (s Settings) withCommand(name string, fallback Factory) Factory {
	return func() (Provider, error) {
		cfg := s.File.Models.Providers[name]
		if retiredProviderConfig(name, cfg) {
			return nil, errRetiredTransport
		}
		if len(cfg.Command) == 0 {
			if fallback == nil {
				return nil, nil
			}
			return fallback()
		}
		return s.localBinary(name, cfg.Command, firstNonEmpty(
			cfg.Model, s.File.Default(name+"_model"), s.File.Default("model")), nil,
			cfg.Values, cfg.TimeoutSeconds, "", cfg.ResponseFormat)
	}
}

func retiredProviderConfig(name string, cfg config.ProviderConfig) bool {
	return name != NameOllama && (cfg.BaseURL != "" || cfg.RetiredCredential)
}

func (s Settings) localBinary(name string, command []string, model string, models []string,
	values map[string]string, timeoutSeconds int, action, responseFormat string) (Provider, error) {
	variables := make(map[string]string, len(values)+1)
	for key, value := range values {
		variables[key] = value
	}
	variables["model"] = model
	return NewLocalBinary(LocalBinaryConfig{
		Name: name, Command: command, Model: model, Models: models, Variables: variables,
		File: s.File.Path, WorkDir: s.RunnerDir,
		Timeout: time.Duration(timeoutSeconds) * time.Second, Action: action, ResponseFormat: responseFormat,
	})
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (s Settings) ollama() Provider {
	cfg := s.File.Models.Providers[NameOllama]
	return NewOllama(OllamaConfig{
		BaseURL: firstNonEmpty(s.env("ROCA_OLLAMA_BASE_URL", "OLLAMA_HOST"), cfg.BaseURL,
			s.File.Default("ollama_base_url")),
		Model: firstNonEmpty(s.env("ROCA_OLLAMA_MODEL", "ROCA_MODEL"), cfg.Model,
			s.File.Default("ollama_model"), s.File.Default("model")),
		KeepAlive: firstNonEmpty(cfg.KeepAlive, s.File.Default("ollama_keep_alive")),
		Think:     cfg.Think,
	})
}

func presentIn(order []string, catalog Catalog) []string {
	kept := make([]string, 0, len(order))
	for _, name := range order {
		if _, ok := catalog[name]; ok {
			kept = append(kept, name)
		}
	}
	return kept
}

func splitList(raw string) []string {
	return strings.Fields(strings.ReplaceAll(raw, ",", " "))
}
