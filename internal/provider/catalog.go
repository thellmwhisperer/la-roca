package provider

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/provider/oauth"
)

// EnvOrder is the provider order asked for out loud, right now. It wins over
// the file and it is treated as a contract: naming what does not exist has to be
// found out at once, not degraded in silence.
const EnvOrder = "ROCA_MODELS_ORDER"

// FileCodexSession is the subscription session's file inside the credentials
// directory.
const FileCodexSession = "codex.json"

// Settings is everything needed to turn a configuration into a live cascade.
type Settings struct {
	File config.File
	// Credentials is the directory subscription sessions live in.
	Credentials string
	// Env reads the environment. It is a field and not a call to os.Getenv so
	// that a test can hand over an environment of its own.
	Env func(key string) string
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

// CodexStore is where the subscription session lives.
func CodexStore(credentials string) oauth.Store {
	return oauth.Store{Path: filepath.Join(credentials, FileCodexSession)}
}

// CodexFlow is the login flow this product performs.
func CodexFlow() oauth.Flow {
	return oauth.Flow{Endpoints: oauth.CodexEndpoints(), Originator: "roca"}
}

// BuildCascade turns the configuration into the resolved provider order.
//
// The order of precedence is the same one the whole product uses: the
// environment, then the file, then the code. What changes with it is not only
// the value but the provenance, and the provenance is what decides whether an
// unknown name degrades or fails.
func BuildCascade(s Settings) (Cascade, error) {
	catalog := s.catalog()
	selection := s.selection(catalog)

	resolved, err := Resolve(selection, catalog)
	if err != nil {
		return Cascade{}, err
	}

	cascade := Cascade{
		Providers: resolved.Providers,
		Disabled:  resolved.Disabled,
		// The config's warnings travel with the cascade: they are about the same
		// file and the operator reads them in the same place.
		Warnings: append(append([]string(nil), s.File.Warnings...), resolved.Warnings...),
	}
	if ms := s.File.Models.TimeoutMS; ms > 0 {
		cascade.Timeout = time.Duration(ms) * time.Millisecond
	}
	if ms := s.File.Models.ProbeMS; ms > 0 {
		cascade.Probe = time.Duration(ms) * time.Millisecond
	}
	return cascade, nil
}

// selection reads the order with its provenance attached.
func (s Settings) selection(catalog Catalog) Selection {
	if raw := s.env(EnvOrder); raw != "" {
		return Selection{Names: splitList(raw), Source: SourceEnv, Key: EnvOrder}
	}
	// A declared order is honoured even when it is empty: an operator who wrote
	// `order = []` said what they meant, and that is not the same as writing no
	// order at all, which is what gets the default.
	if order := s.File.Models.Order; order != nil {
		return Selection{Names: order, Source: SourceConfig, File: s.File.Path, Key: "models.order"}
	}
	// The default order is code, and it only names what this build carries, so
	// it cannot fail; declaring it as code is what keeps that true.
	return Selection{Names: presentIn(DefaultOrder(), catalog), Source: SourceCode}
}

// catalog is what this build can build: the local floor, the subscription
// adapter, the presets, and any provider the operator declared a table for.
func (s Settings) catalog() Catalog {
	catalog := Catalog{
		NameOllama: func() (Provider, error) { return s.ollama(), nil },
		NameCodex:  func() (Provider, error) { return s.codex(), nil },
	}
	for _, name := range PresetNames() {
		catalog[name] = s.openAIFactory(name)
	}
	// A provider with a table of its own is a provider this build knows how to
	// build: one adapter, many providers. It is what lets an operator point at
	// their company's gateway without waiting for a release.
	for name := range s.File.Models.Providers {
		if _, already := catalog[name]; already {
			continue
		}
		catalog[name] = s.openAIFactory(name)
	}
	return catalog
}

func (s Settings) openAIFactory(name string) Factory {
	return func() (Provider, error) {
		cfg := s.File.Models.Providers[name]
		return NewOpenAICompatible(OpenAIConfig{
			Name:    name,
			Preset:  cfg.Preset,
			BaseURL: firstNonEmpty(cfg.BaseURL, s.File.Default(name+"_base_url")),
			Model:   firstNonEmpty(cfg.Model, s.File.Default(name+"_model"), s.File.Default("model")),
			APIKey:  s.credentialFor(name, cfg),
			File:    s.File.Path,
		})
	}
}

// credentialFor looks for the key where an operator may reasonably have left
// it. Order of precedence:
//
//  1. the credential store (`roca login <provider>`),
//  2. api_key written in the config file,
//  3. the environment variable they declared, the provider's usual variable,
//     or this product's own.
//
// The store wins over the file so that logging in replaces a stale key without
// forcing a hand-edit, and the file keeps working for operators who never run
// login.
func (s Settings) credentialFor(name string, cfg config.ProviderConfig) string {
	if s.Credentials != "" {
		if key, err := LoadAPIKey(s.Credentials, name); err == nil && key != "" {
			return key
		}
	}
	if cfg.APIKey != "" {
		return cfg.APIKey
	}
	var candidates []string
	if cfg.APIKeyEnv != "" {
		candidates = append(candidates, cfg.APIKeyEnv)
	}
	if preset, ok := Preset(name); ok {
		if preset.KeyEnv != "" {
			candidates = append(candidates, preset.KeyEnv)
		}
		// A provider known by its model family (GLM, Grok) is exported under that
		// name too, so an operator does not have to learn the vendor's spelling.
		candidates = append(candidates, preset.EnvAliases...)
	}
	candidates = append(candidates, "ROCA_"+envName(name)+"_API_KEY")
	return s.env(candidates...)
}

func (s Settings) ollama() Provider {
	cfg := s.File.Models.Providers[NameOllama]
	return NewOllama(OllamaConfig{
		BaseURL: firstNonEmpty(
			s.env("ROCA_OLLAMA_BASE_URL", "OLLAMA_HOST"),
			cfg.BaseURL,
			s.File.Default("ollama_base_url"),
		),
		Model: firstNonEmpty(
			s.env("ROCA_OLLAMA_MODEL", "ROCA_MODEL"),
			cfg.Model,
			s.File.Default("ollama_model"),
			s.File.Default("model"),
		),
		KeepAlive: firstNonEmpty(cfg.KeepAlive, s.File.Default("ollama_keep_alive")),
	})
}

func (s Settings) codex() Provider {
	cfg := s.File.Models.Providers[NameCodex]
	return NewCodex(CodexConfig{
		Session: oauth.Session{Store: CodexStore(s.Credentials), Flow: CodexFlow()},
		Model: firstNonEmpty(
			s.env("ROCA_CODEX_MODEL"),
			cfg.Model,
			s.File.Default("codex_model"),
		),
		BaseURL: firstNonEmpty(s.env("ROCA_CODEX_BASE_URL"), cfg.BaseURL),
	})
}

// presentIn keeps from an order only what the catalog has. It exists for the
// default order, which is code and therefore a contract: it may not name
// something this build does not carry.
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

func envName(name string) string {
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}
