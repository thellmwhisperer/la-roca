package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type openAICall struct {
	path  string
	auth  string
	model string
}

func openAIServer(t *testing.T, answer string) (*httptest.Server, *[]openAICall) {
	t.Helper()
	var calls []openAICall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := openAICall{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)
			call.model, _ = req["model"].(string)
		}
		calls = append(calls, call)

		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message": map[string]any{"role": "assistant", "content": answer},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestOpenAICompatibleWithoutACredentialIsNotReadyAndNamesTheKeyAndTheFile(t *testing.T) {
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: "https://api.deepseek.com/v1",
		File: "/home/someone/.roca/config.toml",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	readiness := compatible.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("with no credential it cannot be ready")
	}
	for _, piece := range []string{"roca login deepseek", "DEEPSEEK_API_KEY"} {
		if !strings.Contains(readiness.Action, piece) {
			t.Errorf("the action does not name %q: %s", piece, readiness.Action)
		}
	}
}

func TestOpenAICompatibleWithACredentialProbesAndServes(t *testing.T) {
	server, calls := openAIServer(t, "SELECT 1 LIMIT 1")
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: server.URL, APIKey: "sk-secret", Model: "deepseek-chat",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if readiness := compatible.Ready(context.Background()); !readiness.Ready {
		t.Fatalf("it should be ready: %+v", readiness)
	}
	res, err := compatible.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "count"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "SELECT 1 LIMIT 1" {
		t.Fatalf("content %q", res.Content)
	}
	if res.Provider != NameDeepSeek || res.ModelID != "deepseek-chat" {
		t.Fatalf("the provenance does not travel: %+v", res)
	}
	if len(*calls) != 2 {
		t.Fatalf("expected a probe and a request, got %v", *calls)
	}
	if (*calls)[1].auth != "Bearer sk-secret" {
		t.Fatalf("it did not authenticate: %q", (*calls)[1].auth)
	}
	if (*calls)[1].model != "deepseek-chat" {
		t.Fatalf("model %q", (*calls)[1].model)
	}
}

// Without network the frontier is not available and the cascade has to fall to
// the floor unaided.
func TestOpenAICompatibleWithoutNetworkIsNotReadyAndSaysWhere(t *testing.T) {
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: "http://127.0.0.1:1/v1", APIKey: "sk-secret",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	readiness := compatible.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("nothing is listening and it says it is ready")
	}
	if !strings.Contains(readiness.Reason, "unreachable") || !strings.Contains(readiness.Reason, "127.0.0.1:1") {
		t.Fatalf("the reason does not name unreachable and where it looked: %q", readiness.Reason)
	}
}

func TestOpenAICompatibleProbeNamesTheHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()
	compatible, _ := NewOpenAICompatible(OpenAIConfig{Name: "gateway", BaseURL: server.URL, Model: "m", APIKey: "k"})
	readiness := compatible.Ready(t.Context())
	if readiness.Ready || readiness.Reason != "gateway received HTTP status 429" {
		t.Fatalf("readiness = %+v", readiness)
	}
	if readiness.Action != "wait and retry, or use another provider in the order" {
		t.Fatalf("rate-limit action = %q", readiness.Action)
	}
}

func TestOpenAICompatibleProbeNamesATimeout(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	compatible, _ := NewOpenAICompatible(OpenAIConfig{
		Name: "gateway", BaseURL: "https://example.invalid/v1", Model: "m", APIKey: "k", Client: client,
	})
	readiness := compatible.Ready(t.Context())
	if readiness.Ready || readiness.Reason != "request to gateway at example.invalid timed out" {
		t.Fatalf("readiness = %+v", readiness)
	}
}

// One adapter, many providers: the presets are data, not three copies of the
// same client.
func TestThePresetsCarryTheirEndpointAndTheirDefaultModel(t *testing.T) {
	for _, name := range []string{NameDeepSeek, NameZAI, NameXAI} {
		preset, ok := Preset(name)
		if !ok {
			t.Fatalf("there is no preset for %q", name)
		}
		if !strings.HasPrefix(preset.BaseURL, "https://") {
			t.Errorf("%s: base url %q", name, preset.BaseURL)
		}
		if preset.Model == "" {
			t.Errorf("%s: no default model", name)
		}
		if preset.KeyEnv == "" {
			t.Errorf("%s: no environment variable for the credential", name)
		}
	}
}

// The diagnosis names the model-family alias alongside the vendor's variable,
// so an operator who thinks "GLM" or "Grok" is told the spelling that works.
func TestTheDiagnosisNamesTheModelFamilyAlias(t *testing.T) {
	for _, tc := range []struct {
		name, alias string
	}{
		{NameZAI, "ROCA_GLM_API_KEY"},
		{NameXAI, "ROCA_GROK_API_KEY"},
	} {
		compatible, err := NewOpenAICompatible(OpenAIConfig{Name: tc.name})
		if err != nil {
			t.Fatalf("%s: build: %v", tc.name, err)
		}
		readiness := compatible.Ready(context.Background())
		if !strings.Contains(readiness.Action, tc.alias) {
			t.Errorf("%s: the action does not name the alias %q: %s",
				tc.name, tc.alias, readiness.Action)
		}
	}
}

func TestAPresetOnlyFillsInWhatTheOperatorDidNotWrite(t *testing.T) {
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameXAI, Preset: NameXAI, Model: "grok-mini", APIKey: "k",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if compatible.ModelID() != "grok-mini" {
		t.Fatalf("the preset overrode the operator: %q", compatible.ModelID())
	}
	preset, _ := Preset(NameXAI)
	if compatible.BaseURL() != preset.BaseURL {
		t.Fatalf("base url %q", compatible.BaseURL())
	}
}

// A generic provider with no preset and no base_url cannot be built, and saying
// so when it is built is what keeps it from being a mystery at query time.
func TestAGenericProviderWithoutABaseURLCannotBeBuilt(t *testing.T) {
	_, err := NewOpenAICompatible(OpenAIConfig{Name: "mycorp"})
	if err == nil {
		t.Fatal("with no base_url there is nowhere to ask")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("the error does not name the key that is missing: %v", err)
	}
}

// The credential is a key of the operator's: it never travels to any output.
func TestTheCredentialNeverShowsUpInTheDiagnosis(t *testing.T) {
	server, _ := openAIServer(t, "")
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: server.URL, APIKey: "sk-do-not-print-me",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	readiness := compatible.Ready(context.Background())
	blob := readiness.Reason + readiness.Action + readiness.ModelID + compatible.BaseURL()
	if strings.Contains(blob, "sk-do-not-print-me") {
		t.Fatalf("the credential leaked: %s", blob)
	}
}

// `roca models` reads the catalogue the same endpoint Ready probes, but
// keeps the body: it lists every model the credential reaches. The configured
// model is marked by the cascade, not here.
func TestOpenAICompatibleModelsListsTheCatalogue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-secret" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "deepseek-chat"},
			map[string]any{"id": "deepseek-reasoner"},
		}})
	}))
	defer server.Close()
	compatible, err := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: server.URL, APIKey: "sk-secret", Model: "deepseek-chat",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	report := compatible.Models(context.Background())
	if !report.Ready {
		t.Fatalf("with a 2xx and a credential it is ready: %+v", report)
	}
	if got := strings.Join(report.Models, ","); got != "deepseek-chat,deepseek-reasoner" {
		t.Fatalf("models = %q", got)
	}
}

// With no credential there is nothing to list, and the report says why rather
// than inventing a catalogue.
func TestOpenAICompatibleModelsWithoutACredentialIsNotReady(t *testing.T) {
	compatible, err := NewOpenAICompatible(OpenAIConfig{Name: NameDeepSeek, BaseURL: "https://api.deepseek.com/v1"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	report := compatible.Models(context.Background())
	if report.Ready || len(report.Models) != 0 || report.Reason == "" {
		t.Fatalf("no credential should mean no catalogue and a reason: %+v", report)
	}
}

func TestOpenAICompatibleReportsTheStatusOfAFailedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"insufficient balance"}`, http.StatusPaymentRequired)
	}))
	defer server.Close()
	compatible, _ := NewOpenAICompatible(OpenAIConfig{
		Name: NameDeepSeek, BaseURL: server.URL, APIKey: "k",
	})

	if _, err := compatible.Chat(context.Background(), ChatRequest{}); err == nil {
		t.Fatal("a 402 is a failure")
	} else if !strings.Contains(err.Error(), "402") {
		t.Fatalf("the error does not carry the status: %v", err)
	}
}
