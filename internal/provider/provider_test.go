package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// stub is a provider that answers whatever the test tells it to.
type stub struct {
	name     string
	model    string
	ready    Readiness
	models   []string
	answer   string
	fail     error
	delay    time.Duration
	requests int
}

func (s *stub) Name() string    { return s.name }
func (s *stub) ModelID() string { return s.model }

func (s *stub) Models(context.Context) ModelReport {
	return ModelReport{Ready: s.ready.Ready, Models: s.models, Reason: s.ready.Reason}
}

func (s *stub) Ready(context.Context) Readiness {
	r := s.ready
	if r.ModelID == "" {
		r.ModelID = s.model
	}
	return r
}

func (s *stub) Chat(ctx context.Context, _ ChatRequest) (ChatResponse, error) {
	s.requests++
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		}
	}
	if s.fail != nil {
		return ChatResponse{}, s.fail
	}
	return ChatResponse{Content: s.answer, Provider: s.name, ModelID: s.model}, nil
}

func ready(name, model string) *stub {
	return &stub{name: name, model: model, ready: Readiness{Ready: true, ModelID: model}}
}

func notReady(name, reason, action string) *stub {
	return &stub{name: name, ready: Readiness{Reason: reason, Action: action}}
}

func TestOrderResolvesToTheProvidersItNames(t *testing.T) {
	catalog := Catalog{
		"codex":  func() (Provider, error) { return ready("codex", "gpt-5.6-luna"), nil },
		"ollama": func() (Provider, error) { return ready("ollama", "qwen3.5:4b"), nil },
	}

	resolved, err := Resolve(Selection{Names: []string{"codex", "ollama"}, Source: SourceConfig}, catalog)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.Providers) != 2 {
		t.Fatalf("expected two providers, got %d", len(resolved.Providers))
	}
	if resolved.Providers[0].Name() != "codex" || resolved.Providers[1].Name() != "ollama" {
		t.Fatalf("the declared order was not respected: %v", names(resolved.Providers))
	}
	if len(resolved.Warnings) != 0 {
		t.Fatalf("nothing to warn about, got %v", resolved.Warnings)
	}
}

// The provenance travels in the type: an order the operator persisted degrades,
// while one written in code is still a contract and fails.
func TestAnUnknownNameFromConfigDegradesAndNamesTheRemedy(t *testing.T) {
	catalog := Catalog{"ollama": func() (Provider, error) { return ready("ollama", "qwen3.5:4b"), nil }}

	resolved, err := Resolve(Selection{
		Names: []string{"telepathy", "ollama"}, Source: SourceConfig,
		File: "/home/someone/.roca/config.toml", Key: "models.order",
	}, catalog)
	if err != nil {
		t.Fatalf("a config selection must degrade, not fail: %v", err)
	}
	if len(resolved.Providers) != 1 || resolved.Providers[0].Name() != "ollama" {
		t.Fatalf("the known providers must keep loading: %v", names(resolved.Providers))
	}
	if len(resolved.Warnings) != 1 {
		t.Fatalf("expected one warning, got %v", resolved.Warnings)
	}
	warning := resolved.Warnings[0]
	for _, piece := range []string{"telepathy", "/home/someone/.roca/config.toml", "models.order", "ollama"} {
		if !strings.Contains(warning, piece) {
			t.Errorf("the warning does not name %q: %s", piece, warning)
		}
	}
}

func TestAnUnknownNameFromCodeIsStillAContractAndFails(t *testing.T) {
	catalog := Catalog{"ollama": func() (Provider, error) { return ready("ollama", "qwen3.5:4b"), nil }}

	_, err := Resolve(Selection{Names: []string{"telepathy"}, Source: SourceCode}, catalog)
	if err == nil {
		t.Fatal("a selection written in code that names what does not exist has to fail")
	}
	if !strings.Contains(err.Error(), "telepathy") || !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("the error names neither the unknown one nor the available ones: %v", err)
	}
}

func TestADuplicateInTheOrderIsAnError(t *testing.T) {
	catalog := Catalog{"ollama": func() (Provider, error) { return ready("ollama", "qwen3.5:4b"), nil }}

	_, err := Resolve(Selection{Names: []string{"ollama", "ollama"}, Source: SourceConfig}, catalog)
	if err == nil {
		t.Fatal("the same provider twice hides a config confusion and is an error")
	}
	if !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("the error does not name the duplicate: %v", err)
	}
}

// The default order always ends in a provider available on every supported
// platform so platform-specific failures cannot mask the local floor.
func TestTheDefaultOrderEndsInTheProviderThatCanExistAnywhere(t *testing.T) {
	order := DefaultOrder()
	if len(order) == 0 {
		t.Fatal("there is no default order")
	}
	if last := order[len(order)-1]; last != NameOllama {
		t.Fatalf("the default order ends in %q and it has to end in the local floor", last)
	}
}

func TestNoneDisablesTheModelWithoutBeingAnUnknownName(t *testing.T) {
	resolved, err := Resolve(Selection{Names: []string{"none"}, Source: SourceEnv}, Catalog{})
	if err != nil {
		t.Fatalf("`none` disables the model; it is not an unknown name: %v", err)
	}
	if len(resolved.Providers) != 0 {
		t.Fatalf("`none` leaves no provider, got %v", names(resolved.Providers))
	}
	if !resolved.Disabled {
		t.Fatal("the resolution has to declare the model was disabled on purpose")
	}
}

// The fall is by availability, not by exception: each provider is asked Ready
// before use.
func TestPickTakesTheFirstAvailableAndDeclaresWhyTheOthersAreNot(t *testing.T) {
	frontier := notReady("codex", "there is no Codex session", "run `roca login codex`")
	floor := ready("ollama", "qwen3.5:4b")
	cascade := Cascade{Providers: []Provider{frontier, floor}}

	chosen, attempts := cascade.Pick(context.Background())
	if chosen == nil || chosen.Name() != "ollama" {
		t.Fatalf("it did not fall to the floor: %v", chosen)
	}
	if len(attempts) != 2 {
		t.Fatalf("expected two attempts, got %v", attempts)
	}
	if attempts[0].Name != "codex" || attempts[0].Ready {
		t.Fatalf("the first attempt is wrong: %+v", attempts[0])
	}
	if attempts[0].Reason == "" || attempts[0].Action == "" {
		t.Fatalf("a diagnosis that does not name the remedy forces reading code: %+v", attempts[0])
	}
	if !attempts[1].Ready || attempts[1].ModelID != "qwen3.5:4b" {
		t.Fatalf("the chosen one is not declared with its model: %+v", attempts[1])
	}
}

// Once a provider says it is ready it is not swapped in silence: doing so turns
// "the frontier provider is returning 500" into "the answers are odd today".
func TestPickDoesNotAskTheOnesBehindTheChosenOne(t *testing.T) {
	first := ready("codex", "gpt-5.6-luna")
	second := ready("ollama", "qwen3.5:4b")
	cascade := Cascade{Providers: []Provider{first, second}}

	chosen, attempts := cascade.Pick(context.Background())
	if chosen.Name() != "codex" {
		t.Fatalf("chosen %q", chosen.Name())
	}
	if len(attempts) != 1 {
		t.Fatalf("it probed past the titular one: %v", attempts)
	}
}

func TestPickWithNobodyAvailableReturnsNothingAndTheWholeDiagnosis(t *testing.T) {
	cascade := Cascade{Providers: []Provider{
		notReady("codex", "there is no session", "run `roca login codex`"),
		notReady("ollama", "Ollama does not answer", "start it with `ollama serve`"),
	}}

	chosen, attempts := cascade.Pick(context.Background())
	if chosen != nil {
		t.Fatalf("nobody was available and it chose %q", chosen.Name())
	}
	if len(attempts) != 2 {
		t.Fatalf("the diagnosis has to name every provider tried: %v", attempts)
	}
}

// A provider's fragility never takes down a query: the request is bounded and
// what times out is a declared failure, not a hang.
func TestChatIsBoundedByTheDeclaredTimeout(t *testing.T) {
	slow := ready("slow", "m")
	slow.delay = 2 * time.Second
	cascade := Cascade{Providers: []Provider{slow}, Timeout: 30 * time.Millisecond}

	start := time.Now()
	_, err := cascade.Chat(context.Background(), slow, ChatRequest{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a request past the budget has to fail, not wait")
	}
	if elapsed > time.Second {
		t.Fatalf("the timeout did not bound anything: %v", elapsed)
	}
}

func TestChatReturnsWhatTheProviderAnswered(t *testing.T) {
	answering := ready("ollama", "qwen3.5:4b")
	answering.answer = "SELECT 1"
	cascade := Cascade{Providers: []Provider{answering}, Timeout: time.Second}

	res, err := cascade.Chat(context.Background(), answering, ChatRequest{})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "SELECT 1" {
		t.Fatalf("content %q", res.Content)
	}
	if res.LatencyMS < 0 {
		t.Fatalf("latency %d", res.LatencyMS)
	}
}

func TestProbeCauseDistinguishesCancellationFromReachability(t *testing.T) {
	if got := unreachable("gateway", context.Canceled); got != "request to gateway was canceled" {
		t.Fatalf("cause = %q", got)
	}
}

// `roca models` asks every provider for its catalogue and marks the model the
// cascade would actually use, so an operator can see what is on offer without
// firing a question. The provenance of the selected model travels from
// ModelID, the catalogue from Models.
func TestCascadeModelsListsEachProviderAndMarksTheSelected(t *testing.T) {
	codex := &stub{name: "codex", model: "gpt-5.6-luna",
		ready: Readiness{Ready: true}, models: []string{"gpt-5.6-luna"}}
	ollama := &stub{name: "ollama", model: "qwen3.5:4b",
		ready: Readiness{Ready: true}, models: []string{"qwen3.5:4b", "gemma4:12b"}}
	cascade := Cascade{Providers: []Provider{codex, ollama}}

	listings := cascade.Models(context.Background())
	if len(listings) != 2 {
		t.Fatalf("expected two listings, got %d: %+v", len(listings), listings)
	}
	if listings[0].Name != "codex" || listings[0].Selected != "gpt-5.6-luna" {
		t.Fatalf("the first listing lost its name or selected model: %+v", listings[0])
	}
	if !listings[0].Ready || len(listings[0].Models) != 1 {
		t.Fatalf("codex did not report a ready catalogue: %+v", listings[0])
	}
	if listings[1].Selected != "qwen3.5:4b" || len(listings[1].Models) != 2 {
		t.Fatalf("ollama did not report its models with the selected marked: %+v", listings[1])
	}
}

// A provider that cannot be reached is still listed, with its reason: `roca
// models` keeps going past a failure instead of aborting on the first one.
func TestCascadeModelsCarriesTheReasonWhenAProviderIsUnreachable(t *testing.T) {
	down := &stub{name: "codex", ready: Readiness{Reason: "there is no Codex session"}}
	cascade := Cascade{Providers: []Provider{down}}

	listings := cascade.Models(context.Background())
	if len(listings) != 1 || listings[0].Ready {
		t.Fatalf("an unreachable provider must be listed as not ready: %+v", listings)
	}
	if listings[0].Reason == "" {
		t.Fatal("the reason has to travel so the operator reads what to do")
	}
}

func TestAProviderThatFailsToBuildIsAWarningAndNotACrash(t *testing.T) {
	catalog := Catalog{
		"broken": func() (Provider, error) { return nil, errors.New("no api_key") },
		"ollama": func() (Provider, error) { return ready("ollama", "qwen3.5:4b"), nil },
	}
	resolved, err := Resolve(Selection{Names: []string{"broken", "ollama"}, Source: SourceConfig}, catalog)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolved.Providers) != 1 {
		t.Fatalf("the healthy ones keep loading: %v", names(resolved.Providers))
	}
	if len(resolved.Warnings) != 1 || !strings.Contains(resolved.Warnings[0], "no api_key") {
		t.Fatalf("the warning does not carry the why: %v", resolved.Warnings)
	}
}

func names(providers []Provider) []string {
	var out []string
	for _, p := range providers {
		out = append(out, p.Name())
	}
	return out
}
