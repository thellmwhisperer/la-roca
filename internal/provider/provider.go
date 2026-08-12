// Package provider is the model adapters and the cascade that chooses between
// them.
//
// With no explicit order, the decision is detected local agent CLIs in stable
// preset order, then Ollama as the local floor. An environment or file order is
// preserved as the operator wrote it; keyword rescue belongs to the service
// after this provider cascade rather than pretending to be a provider.
//
// Three things this package holds on to, and each one was paid for:
//
//   - **The fall is normally by availability, not by exception.** Before using a
//     provider it is asked Ready. The factory order permits one narrower case:
//     a detected local CLI whose first real request disproves its usable session
//     fails forward without paying for a separate inference probe.
//   - **The provenance travels in the type.** A Selection carries where the
//     order came from, so a name the operator persisted degrades with a warning
//     and one written in code still fails. There is no way to lose the
//     provenance along the way because there is no constructor without it.
//   - **Zero provider SDKs.** Network adapters speak HTTP with net/http; local
//     adapters run an argv template with os/exec. An SDK puts its dependency
//     chain and version cadence inside the binary to save two hundred lines.
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The names this build knows. They are normalized: lower case and hyphens.
const (
	NameOllama = "ollama"
	NameCodex  = "codex"
	NameClaude = "claude"
)

// DefaultTimeout bounds a request to a provider. It leaves room for slower
// local models because an unbounded request is a hung command.
const DefaultTimeout = 90 * time.Second

const ProbeTimeout = 3 * time.Second

// Provider is everything the cascade needs to know about a provider.
type Provider interface {
	// Name is normalized: lower case and hyphens.
	Name() string
	// ModelID is the concrete model that is going to answer.
	ModelID() string
	// Ready answers whether it can be used right now.
	Ready(ctx context.Context) Readiness
	// Models is its catalogue: which models the transport or the local runtime
	// reaches. A provider that cannot be reached reports not ready with a reason,
	// so `roca models` lists what it can and keeps going past what it cannot.
	Models(ctx context.Context) ModelReport
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// StreamingProvider is the optional transport capability used for prose. SQL
// still travels through Chat as one statement; an interpretation can expose
// text as it arrives without making streaming mandatory for every adapter.
type StreamingProvider interface {
	ChatStream(context.Context, ChatRequest, func(string)) (ChatResponse, error)
}

// Readiness is the answer to "can I use you right now?".
//
// Reason and Action are not decoration: they are what `roca doctor` prints, and
// every diagnosis must name its remedy.
type Readiness struct {
	Ready   bool   `json:"ready"`
	ModelID string `json:"model,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Action  string `json:"action,omitempty"`
}

// ModelReport is the answer to "which models does this provider offer?". It is
// the catalogue half of `roca models`, where Ready is the availability half: a
// provider that cannot be reached reports Ready false with a Reason, and the
// command keeps going past it instead of failing the whole list.
type ModelReport struct {
	Ready  bool     `json:"ready"`
	Models []string `json:"models,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

// Message is a turn of the conversation handed to the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Roles this product uses. It does not converse: it asks for one SQL statement,
// and at most it shows the model the engine's verdict on the one it wrote.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ChatRequest is what is asked of a provider.
type ChatRequest struct {
	Messages  []Message
	MaxTokens int
}

// ChatResponse is what it answered, with the provenance of who answered.
type ChatResponse struct {
	Content   string
	Provider  string
	ModelID   string
	LatencyMS int64
}

// Attempt is what happened with one provider of the order. It is the answer's
// provenance and `roca doctor`'s report: which ones were tried and why each one
// that did not serve did not.
type Attempt struct {
	Name    string `json:"provider"`
	Ready   bool   `json:"ready"`
	ModelID string `json:"model,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Action  string `json:"action,omitempty"`
}

// ModelsListing is one provider's catalogue for `roca models`: the models the
// transport or the local runtime reaches, with the model the cascade would
// actually use (ModelID) marked as Selected. It parallels Attempt, which is the
// availability report this is the catalogue report.
type ModelsListing struct {
	Name     string   `json:"provider"`
	Selected string   `json:"selected,omitempty"`
	Ready    bool     `json:"ready"`
	Models   []string `json:"models,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// Source is where a provider order came from. A caller that reads the
// operator's selection and passes it on loses the
// degradation unless the provenance travels with the value.
type Source int

// The three provenances an order can have.
const (
	// SourceCode is a contract: what it names has to exist.
	SourceCode Source = iota
	// SourceConfig is data the operator persisted, and it outlives the release
	// that understood it: what this build does not know is a warning.
	SourceConfig
	// SourceEnv is the operator asking for something right now, out loud. It is
	// treated as a contract: if they name what does not exist, they have to
	// find out at once.
	SourceEnv
)

func (s Source) String() string {
	switch s {
	case SourceConfig:
		return "config"
	case SourceEnv:
		return "environment"
	default:
		return "code"
	}
}

// Selection is a provider order with its provenance and with where it is
// written, because every message to the operator names the key and the file,
// never a TOML table.
type Selection struct {
	Names  []string
	Source Source
	File   string
	Key    string
}

// disablingNames turn the model off without being unknown names, so an
// operator does not have to invent an empty list.
var disablingNames = map[string]bool{
	"none": true, "off": true, "disabled": true, "false": true, "0": true,
}

// Factory builds a provider. It returns an error when the configuration does
// not let it be built, and that error is a warning, never a crash.
type Factory func() (Provider, error)

// Catalog is what this build knows how to build, by name.
type Catalog map[string]Factory

// Resolved is the outcome of resolving an order.
type Resolved struct {
	Providers []Provider
	Warnings  []string
	// Disabled says the operator turned the model off on purpose. It is not the
	// same as having nothing available, and the message to the operator differs.
	Disabled bool
}

// DefaultOrder is the effective order with no config: detected local agent CLI
// binaries first and the local floor last. Keyword search remains the rescue
// after this provider order rather than pretending to be a model provider.
//
// The last element is always a provider that can exist on any supported
// platform, so platform-specific providers cannot hide a usable local floor.
func DefaultOrder(lookPath LookPathFunc) []string {
	return defaultOrderFromDetected(DetectedCommandPresets(lookPath))
}

func defaultOrderFromDetected(detected []string) []string {
	order := append([]string(nil), detected...)
	return append(order, NameOllama)
}

// Resolve turns a selection into providers.
//
// Resolve preserves two validation guarantees plus the provenance rule:
//   - a duplicate is an error, always: the same provider twice hides a config
//     confusion, and guessing which of the two was meant is answering a
//     different question;
//   - an unknown name is an error when the selection is a contract and a warning
//     naming the remedy when it is data the operator persisted.
func Resolve(sel Selection, catalog Catalog) (Resolved, error) {
	var res Resolved
	seen := make(map[string]bool, len(sel.Names))

	for _, raw := range sel.Names {
		name := normalize(raw)
		if name == "" {
			continue
		}
		if disablingNames[name] {
			res.Disabled = true
			continue
		}
		if seen[name] {
			return Resolved{}, fmt.Errorf(
				"the provider order names %q twice%s: leave it once, in the position you want it to be tried",
				name, where(sel))
		}
		seen[name] = true

		build, known := catalog[name]
		if !known {
			if sel.Source == SourceConfig {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"this version does not know the provider %q%s: it is ignored and the rest keep working. Available providers: %s",
					name, where(sel), strings.Join(available(catalog), ", ")))
				continue
			}
			return Resolved{}, fmt.Errorf(
				"this version does not know the provider %q%s. Available providers: %s",
				name, where(sel), strings.Join(available(catalog), ", "))
		}

		built, err := build()
		if err == nil && built == nil {
			err = errors.New("this build produced no adapter for it")
		}
		if err != nil {
			// A provider that cannot be built is one provider less, never a
			// command that does not run: the fragility of one provider does not
			// take down a query. A factory that returns nothing at all is the
			// same case: nothing nil ever reaches the cascade.
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"the provider %q cannot be used: %v", name, err))
			continue
		}
		res.Providers = append(res.Providers, built)
	}
	// An order that leaves no provider is not turned into Disabled here: an
	// installation with nothing available and one that was turned off on purpose
	// get different messages, and only the operator's own words tell them apart.
	return res, nil
}

// where names the file and the key when the selection carries them, because a
// warning that does not say where the value is written makes the operator hunt
// for it.
func where(sel Selection) string {
	switch {
	case sel.File != "" && sel.Key != "":
		return fmt.Sprintf(" (key %s of %s)", sel.Key, sel.File)
	case sel.File != "":
		return fmt.Sprintf(" (in %s)", sel.File)
	case sel.Key != "":
		return fmt.Sprintf(" (key %s)", sel.Key)
	case sel.Source == SourceEnv:
		return " (in the environment)"
	}
	return ""
}

func available(catalog Catalog) []string {
	out := make([]string, 0, len(catalog))
	for name := range catalog {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func normalize(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

func CanonicalName(name string) string { return normalize(name) }

// Cascade is the resolved order, ready to serve.
type Cascade struct {
	Providers []Provider
	// DetectedBinaries names shipped local CLI presets found on PATH. It is
	// diagnostic metadata even when an explicit provider order is active.
	DetectedBinaries []string
	// FallbackDiagnostics are absent shipped binaries. They are appended
	// only when every actual provider fails, so keyword degradation names the
	// semantic transports that were missing without putting them in the order.
	FallbackDiagnostics []Attempt
	// FactoryDefault says the order was constructed from PATH rather than read
	// from the environment or configuration.
	FactoryDefault bool
	// Timeout bounds a model request. Zero is DefaultTimeout.
	Timeout time.Duration
	// Probe bounds the availability question. Zero is ProbeTimeout.
	Probe time.Duration
	// Warnings are what resolving the order had to say.
	Warnings []string
	// Disabled says the operator turned the model off on purpose.
	Disabled bool
}

func (c Cascade) ask(ctx context.Context, p Provider) (Attempt, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, c.probe())
	readiness := p.Ready(probeCtx)
	cancel()

	return Attempt{
		Name: p.Name(), Ready: readiness.Ready, ModelID: readiness.ModelID,
		Reason: readiness.Reason, Action: readiness.Action,
	}, readiness.Ready
}

// Pick returns the first available provider and the record of everything tried.
//
// It stops at the first yes: asking the ones behind the titular costs latency
// on every single query and answers a question nobody asked.
func (c Cascade) Pick(ctx context.Context) (Provider, []Attempt) {
	return c.pick(ctx, 0)
}

func (c Cascade) PickAfter(ctx context.Context, providerName string) (Provider, []Attempt) {
	start := len(c.Providers)
	for i, candidate := range c.Providers {
		if candidate.Name() == providerName {
			start = i + 1
			break
		}
	}
	return c.pick(ctx, start)
}

func (c Cascade) pick(ctx context.Context, start int) (Provider, []Attempt) {
	var attempts []Attempt
	for _, p := range c.Providers[start:] {
		attempt, ready := c.ask(ctx, p)
		attempts = append(attempts, attempt)
		if ready {
			return p, attempts
		}
	}
	return nil, c.CompleteDiagnostics(attempts)
}

func (c Cascade) CompleteDiagnostics(attempts []Attempt) []Attempt {
	seen := make(map[string]bool, len(attempts))
	for _, attempt := range attempts {
		seen[attempt.Name] = true
	}
	for _, diagnostic := range c.FallbackDiagnostics {
		if !seen[diagnostic.Name] {
			attempts = append(attempts, diagnostic)
		}
	}
	return attempts
}

func (c Cascade) Diagnose(ctx context.Context) []Attempt {
	attempts := make([]Attempt, 0, len(c.Providers))
	for _, p := range c.Providers {
		probeCtx, cancel := context.WithTimeout(ctx, c.probeFor(p))
		readiness := p.Ready(probeCtx)
		if diagnostic, ok := p.(interface {
			DiagnoseReady(context.Context) Readiness
		}); ok {
			readiness = diagnostic.DiagnoseReady(probeCtx)
		}
		cancel()
		attempt := Attempt{Name: p.Name(), Ready: readiness.Ready,
			ModelID: readiness.ModelID, Reason: readiness.Reason, Action: readiness.Action}
		attempts = append(attempts, attempt)
	}
	return attempts
}

// Models asks every provider for its catalogue, bounded by the same probe that
// bounds availability. It is the whole of `roca models`: each provider is asked
// in turn, a failure becomes a not-ready listing with its reason, and the
// command never aborts on the first provider that does not answer.
func (c Cascade) Models(ctx context.Context) []ModelsListing {
	listings := make([]ModelsListing, 0, len(c.Providers))
	for _, p := range c.Providers {
		report := c.askModels(ctx, p)
		listings = append(listings, ModelsListing{
			Name: p.Name(), Selected: p.ModelID(),
			Ready: report.Ready, Models: report.Models, Reason: report.Reason,
		})
	}
	return listings
}

func (c Cascade) askModels(ctx context.Context, p Provider) ModelReport {
	modelsCtx, cancel := context.WithTimeout(ctx, c.probeFor(p))
	defer cancel()
	return p.Models(modelsCtx)
}

func (c Cascade) probeFor(p Provider) time.Duration {
	if c.Probe > 0 {
		return c.Probe
	}
	if timed, ok := p.(interface{ RequestTimeout() time.Duration }); ok {
		return timed.RequestTimeout()
	}
	return ProbeTimeout
}

// Chat asks the chosen provider, with the budget bounded. What goes past the
// budget is a declared failure, never a command that never comes back.
func (c Cascade) Chat(ctx context.Context, p Provider, req ChatRequest) (ChatResponse, error) {
	return c.chat(ctx, p, func(callCtx context.Context) (ChatResponse, error) {
		return p.Chat(callCtx, req)
	})
}

func (c Cascade) chat(ctx context.Context, p Provider,
	ask func(context.Context) (ChatResponse, error)) (ChatResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeoutFor(p))
	defer cancel()

	start := time.Now()
	res, err := ask(callCtx)
	if err != nil {
		return ChatResponse{}, err
	}
	if res.Provider == "" {
		res.Provider = p.Name()
	}
	if res.ModelID == "" {
		res.ModelID = p.ModelID()
	}
	if res.LatencyMS == 0 {
		res.LatencyMS = time.Since(start).Milliseconds()
	}
	return res, nil
}

// ChatStream streams when the chosen adapter supports it and otherwise emits
// the completed answer once. The caller therefore has one graceful contract
// for both streaming and buffered providers.
func (c Cascade) ChatStream(ctx context.Context, p Provider, req ChatRequest,
	onDelta func(string)) (ChatResponse, error) {
	return c.chat(ctx, p, func(callCtx context.Context) (ChatResponse, error) {
		if streaming, ok := p.(StreamingProvider); ok {
			return streaming.ChatStream(callCtx, req, onDelta)
		}
		res, err := p.Chat(callCtx, req)
		if err == nil && onDelta != nil && res.Content != "" {
			onDelta(res.Content)
		}
		return res, err
	})
}

func (c Cascade) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

func (c Cascade) timeoutFor(p Provider) time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	if timed, ok := p.(interface{ RequestTimeout() time.Duration }); ok {
		return timed.RequestTimeout()
	}
	return DefaultTimeout
}

func (c Cascade) probe() time.Duration {
	if c.Probe <= 0 {
		return ProbeTimeout
	}
	return c.Probe
}
