package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/oauth"
)

// codexBackend plays the vendor's Responses endpoint, which answers with a
// server-sent event stream.
func codexBackend(t *testing.T, events []string) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			fmt.Fprint(w, event)
		}
	}))
	t.Cleanup(server.Close)
	return server, &seen
}

func sse(name string, payload map[string]any) string {
	body, _ := json.Marshal(payload)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, body)
}

func liveSession(t *testing.T) oauth.Session {
	t.Helper()
	store := oauth.Store{Path: filepath.Join(t.TempDir(), "codex.json")}
	if err := store.Save(oauth.Token{
		AccessToken: "at", RefreshToken: "rt", AccountID: "acct-7",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed the session: %v", err)
	}
	return oauth.Session{Store: store}
}

func TestCodexWithoutASessionNamesTheExactCommand(t *testing.T) {
	codex := NewCodex(CodexConfig{
		Session: oauth.Session{Store: oauth.Store{Path: filepath.Join(t.TempDir(), "codex.json")}},
	})

	readiness := codex.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("there is no session and it says it is ready")
	}
	if !strings.Contains(readiness.Action, "roca login codex") {
		t.Fatalf("the action does not name the exact command: %q", readiness.Action)
	}
}

func TestCodexWithASessionAndReachableVendorIsReady(t *testing.T) {
	server, _ := codexBackend(t, nil)
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL})

	readiness := codex.Ready(context.Background())
	if !readiness.Ready {
		t.Fatalf("it should be ready: %+v", readiness)
	}
	if readiness.ModelID != DefaultCodexModel {
		t.Fatalf("model %q", readiness.ModelID)
	}
}

// With a session and no network the frontier is not available, and the cascade
// has to fall to the local floor unaided (F07-02).
func TestCodexWithASessionAndNoNetworkIsNotReady(t *testing.T) {
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: "http://127.0.0.1:1"})

	readiness := codex.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("there is no network and it says it is ready")
	}
	if !strings.Contains(readiness.Reason, "unreachable") {
		t.Fatalf("it does not name the cause: %q", readiness.Reason)
	}
}

// The subscription does not enumerate its catalogue over an open endpoint the
// way a key provider does, so `roca models` reports the model the session is
// configured to use when that session is usable. That is the honest answer to
// "which model can I reach": the one the subscription serves.
func TestCodexModelsReportsTheConfiguredModelWhenUsable(t *testing.T) {
	server, _ := codexBackend(t, nil)
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL, Model: "gpt-5.6-luna"})

	report := codex.Models(context.Background())
	if !report.Ready {
		t.Fatalf("a usable session lists its model: %+v", report)
	}
	if len(report.Models) != 1 || report.Models[0] != "gpt-5.6-luna" {
		t.Fatalf("the configured model is the one the subscription serves: %+v", report)
	}
}

// With no session there is nothing to reach, and the report carries the same
// reason Ready does so `roca models` can show it without a second probe shape.
func TestCodexModelsWithoutASessionCarriesTheReason(t *testing.T) {
	codex := NewCodex(CodexConfig{})
	report := codex.Models(context.Background())
	if report.Ready || len(report.Models) != 0 || report.Reason == "" {
		t.Fatalf("no session should mean no list and a reason: %+v", report)
	}
}

func TestCodexRefreshesARejectedAccessTokenAndPersistsIt(t *testing.T) {
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh","expires_in":3600}`)
	}))
	defer identity.Close()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer backend.Close()
	store := oauth.Store{Path: filepath.Join(t.TempDir(), "codex.json")}
	if err := store.Save(oauth.Token{AccessToken: "rejected", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	session := oauth.Session{Store: store, Flow: oauth.Flow{Endpoints: oauth.Endpoints{
		Token: identity.URL, ClientID: "client",
	}, Client: identity.Client()}}
	readiness := NewCodex(CodexConfig{Session: session, BaseURL: backend.URL}).Ready(t.Context())
	if !readiness.Ready {
		t.Fatalf("refreshable session = %+v", readiness)
	}
	stored, err := store.Load()
	if err != nil || stored.AccessToken != "fresh" {
		t.Fatalf("stored token = %+v, err=%v", stored, err)
	}
}

func TestCodexNamesAnExpiredUnrefreshableSession(t *testing.T) {
	store := oauth.Store{Path: filepath.Join(t.TempDir(), "codex.json")}
	if err := store.Save(oauth.Token{AccessToken: "expired", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	readiness := NewCodex(CodexConfig{Session: oauth.Session{Store: store}}).Ready(t.Context())
	want := "the Codex session is not usable: refresh the expired access token: this session has no refresh token: log in again"
	if readiness.Ready || readiness.Reason != want {
		t.Fatalf("readiness = %+v, want reason %q", readiness, want)
	}
}

func TestCodexReadsTheAnswerOutOfTheEventStream(t *testing.T) {
	server, headers := codexBackend(t, []string{
		sse("response.created", map[string]any{}),
		sse("response.output_text.delta", map[string]any{"delta": "SELECT count(*) "}),
		sse("response.output_text.delta", map[string]any{"delta": "FROM memories LIMIT 1"}),
		sse("response.completed", map[string]any{}),
	})
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL})

	res, err := codex.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "you are a SQL assistant"},
			{Role: RoleUser, Content: "how many memories are there"},
		},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "SELECT count(*) FROM memories LIMIT 1" {
		t.Fatalf("content %q", res.Content)
	}
	if res.Provider != NameCodex {
		t.Fatalf("provider %q", res.Provider)
	}
	if got := headers.Get("Authorization"); got != "Bearer at" {
		t.Fatalf("it did not authenticate: %q", got)
	}
	if got := headers.Get("chatgpt-account-id"); got != "acct-7" {
		t.Fatalf("it did not address the account: %q", got)
	}
}

// The adapter transports, it does not interpret: a prose answer that quotes a
// fenced block arrives whole. Clipping to the first fence at this layer is what
// turned a full interpretation into the single word "atm" (2026-08-10).
func TestCodexKeepsProseAroundAFencedBlock(t *testing.T) {
	prose := "The details: ```\natm\n``` and the channel has 97 subs."
	server, _ := codexBackend(t, []string{
		sse("response.output_text.delta", map[string]any{"delta": prose}),
	})
	res, err := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL}).
		Chat(context.Background(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "details"}}})
	if err != nil || res.Content != prose {
		t.Fatalf("the answer was clipped: %q (err=%v)", res.Content, err)
	}
}

// Some answers come whole in the completion event and never as deltas.
func TestCodexReadsTheAnswerOutOfTheCompletionEvent(t *testing.T) {
	server, _ := codexBackend(t, []string{
		sse("response.completed", map[string]any{
			"response": map[string]any{
				"output": []any{map[string]any{
					"type": "message",
					"content": []any{map[string]any{
						"type": "output_text", "text": "SELECT 1 LIMIT 1",
					}},
				}},
			},
		}),
	})
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL})

	res, err := codex.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "one"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "SELECT 1 LIMIT 1" {
		t.Fatalf("content %q", res.Content)
	}
}

func TestCodexReportsTheVendorsFailureEvent(t *testing.T) {
	server, _ := codexBackend(t, []string{
		sse("response.failed", map[string]any{
			"response": map[string]any{
				"error": map[string]any{"message": "usage limit reached"},
			},
		}),
	})
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL})

	_, err := codex.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("a failed response is a failure")
	}
	if !strings.Contains(err.Error(), "usage limit reached") {
		t.Fatalf("the error does not carry the vendor's reason: %v", err)
	}
}

func TestCodexSendsTheSystemMessageAsInstructions(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		fmt.Fprint(w, sse("response.output_text.delta", map[string]any{"delta": "SELECT 1"}))
	}))
	defer server.Close()
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: server.URL})

	if _, err := codex.Chat(context.Background(), ChatRequest{Messages: []Message{
		{Role: RoleSystem, Content: "the rules"},
		{Role: RoleUser, Content: "the question"},
		{Role: RoleAssistant, Content: "the rejected SQL"},
		{Role: RoleUser, Content: "the correction"},
	}}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if got, _ := body["instructions"].(string); got != "the rules" {
		t.Fatalf("instructions %q", got)
	}
	if got, _ := body["model"].(string); got != DefaultCodexModel {
		t.Fatalf("model %q", got)
	}
	if store, ok := body["store"].(bool); !ok || store {
		t.Fatalf("this product does not leave its queries stored at the vendor: store=%v", body["store"])
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatal("the ChatGPT Codex backend rejects max_output_tokens")
	}
	input := body["input"].([]any)
	assistant := input[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if assistant["type"] != "output_text" {
		t.Fatalf("assistant retry history uses content type %q, want output_text", assistant["type"])
	}
}

func TestCodexDefaultsToTheModelTheDecisionNames(t *testing.T) {
	if DefaultCodexModel != "gpt-5.6-luna" {
		t.Fatalf("the default model is %q and the decision names gpt-5.6-luna", DefaultCodexModel)
	}
	if got := NewCodex(CodexConfig{Model: "gpt-5.6-mini"}).ModelID(); got != "gpt-5.6-mini" {
		t.Fatalf("it overrode the operator: %q", got)
	}
}

func TestCodexNeverPrintsTheCredential(t *testing.T) {
	codex := NewCodex(CodexConfig{Session: liveSession(t), BaseURL: "http://127.0.0.1:1"})
	readiness := codex.Ready(context.Background())
	if strings.Contains(readiness.Reason+readiness.Action, "at") &&
		strings.Contains(readiness.Reason+readiness.Action, "rt") &&
		strings.Contains(readiness.Reason+readiness.Action, "Bearer") {
		t.Fatalf("the credential leaked: %+v", readiness)
	}
}
