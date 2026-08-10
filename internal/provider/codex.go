/*
@overview Codex subscription adapter. ~320 lines, 10 public symbols, OAuth Responses transport.

	READING GUIDE
	-------------
	1. Start at Codex.Chat for the request/response contract
	2. Read Codex.Ready for provider selection behavior
	3. Read readResponseStream for SSE decoding

	MAIN FLOW
	---------
	NewCodex -> Ready -> Chat -> splitBySystemMessage -> readResponseStream -> ChatResponse

	PUBLIC API
	----------
	DefaultCodexModel, DefaultCodexBaseURL   Vendor defaults
	CodexConfig, NewCodex                    Adapter construction
	Codex and its Name, ModelID, HasCredential, Ready, Chat methods

	INTERNALS
	---------
	probe, authorize, splitBySystemMessage, joinRules, readResponseStream

@exports DefaultCodexModel, DefaultCodexBaseURL, CodexConfig, Codex, NewCodex, Name, ModelID, HasCredential, Ready, Chat
@deps internal/oauth for subscription sessions; net/http for Responses; encoding/json and bufio for SSE
*/
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/oauth"
)

// -- 1/4 HELPER · Defaults and construction --

// DefaultCodexModel is the model the subscription adapter asks for. It is the
// decision of 2026-08-05: Codex is the most generous plan in tokens
// and Luna is the model of the moment.
const DefaultCodexModel = "gpt-5.6-luna"

// DefaultCodexBaseURL is the vendor's endpoint for subscription sessions. It is
// not the platform API: a subscription is not a platform key and it does not
// enter through the same door.
const DefaultCodexBaseURL = "https://chatgpt.com/backend-api/codex"

// CodexConfig is what the configuration says about the subscription adapter.
type CodexConfig struct {
	Session oauth.Session
	Model   string
	BaseURL string
	Client  *http.Client
}

// Codex is the subscription adapter: it authenticates with the session `roca
// login codex` left on disk, renews it in silence and asks the vendor over its
// Responses protocol.
//
// The risk is taken with eyes open and the mitigation is in the shape: a
// vendor's OAuth flow changes with no notice, so this adapter fails clearly and
// the cascade degrades to the next provider or to the local floor. It never
// takes down a query.
type Codex struct {
	session oauth.Session
	model   string
	baseURL string
	client  *http.Client
}

// NewCodex builds the subscription adapter.
func NewCodex(cfg CodexConfig) *Codex {
	return &Codex{
		session: cfg.Session,
		model:   firstNonEmpty(cfg.Model, DefaultCodexModel),
		baseURL: normalizeBaseURL(firstNonEmpty(cfg.BaseURL, DefaultCodexBaseURL)),
		client:  orDefaultClient(cfg.Client),
	}
}

// Name is the normalized name.
func (c *Codex) Name() string { return NameCodex }

// ModelID is the model that is going to answer.
func (c *Codex) ModelID() string { return c.model }

func (c *Codex) HasCredential() bool { return c.session.Store.Exists() }

// Models reports the model the subscription is configured to use when the
// session is usable. The Responses endpoint the subscription speaks does not
// enumerate a catalogue the way a key provider's /models does, so the honest
// answer to "which model can I reach" is the one the session serves. A session
// that is not usable carries the same reason Ready would, so `roca models` shows
// it without a second probe shape.
func (c *Codex) Models(ctx context.Context) ModelReport {
	readiness := c.Ready(ctx)
	if !readiness.Ready {
		return ModelReport{Reason: readiness.Reason}
	}
	return ModelReport{Ready: true, Models: []string{c.model}}
}

// -/ 1/4

// -- 2/4 HELPER · Codex.Ready --

func (c *Codex) Ready(ctx context.Context) Readiness {
	if !c.session.Store.Exists() {
		return Readiness{
			ModelID: c.model,
			Reason:  "there is no Codex session on this machine",
			Action:  "log in with `roca login codex`",
		}
	}
	token, err := c.session.Token(ctx)
	if err != nil {
		return Readiness{
			ModelID: c.model,
			Reason:  fmt.Sprintf("the Codex session is not usable: %v", err),
			Action:  "log in again with `roca login codex`",
		}
	}
	status, err := c.probe(ctx, token)
	if err != nil {
		return Readiness{
			ModelID: c.model,
			Reason:  unreachable("Codex at "+hostFromURL(c.baseURL), err),
			Action:  "check the network, or let the cascade fall to the local floor",
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		token, err = c.session.Refresh(ctx)
		if err != nil {
			return Readiness{ModelID: c.model,
				Reason: fmt.Sprintf("the Codex access token received HTTP status %d and could not be refreshed: %v", status, err),
				Action: "log in again with `roca login codex`"}
		}
		status, err = c.probe(ctx, token)
		if err != nil {
			return Readiness{ModelID: c.model,
				Reason: unreachable("Codex at "+hostFromURL(c.baseURL), err),
				Action: "check the network, or let the cascade fall to the local floor"}
		}
	}
	if status < 200 || status >= 300 {
		return Readiness{ModelID: c.model,
			Reason: fmt.Sprintf("Codex received HTTP status %d", status),
			Action: "try again later, or let the cascade fall to the local floor"}
	}
	return Readiness{Ready: true, ModelID: c.model}
}

func (c *Codex) probe(ctx context.Context, token oauth.Token) (int, error) {
	if token.Expired(time.Now()) {
		return 0, fmt.Errorf("codex token expired")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/responses", http.NoBody)
	if err != nil {
		return 0, err
	}
	c.authorize(req, token)
	req.Header.Set("Accept", "text/event-stream")
	res, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer drain(res)
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
		return http.StatusOK, nil
	}
	return res.StatusCode, nil
}

// -/ 2/4

// -- 3/4 CORE · Codex.Chat -- <- START HERE

// Chat asks the vendor over its Responses protocol.
//
// The system message travels as `instructions` and the question as the input,
// which is the shape that protocol has; the answer comes back as a stream of
// events even though nothing here streams, because that endpoint has no other
// mode. `store` goes false: the operator's questions about their own memory are
// not left in a vendor's account.
func (c *Codex) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	token, err := c.session.Token(ctx)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("the Codex session is not usable: %w", err)
	}

	instructions, input := splitBySystemMessage(req.Messages)
	body, err := json.Marshal(map[string]any{
		"model":        c.model,
		"instructions": instructions,
		"input":        input,
		"stream":       true,
		"store":        false,
	})
	if err != nil {
		return ChatResponse{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses",
		bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	c.authorize(request, token)
	request.Header.Set("OpenAI-Beta", "responses=experimental")
	request.Header.Set("originator", "roca")

	res, err := c.client.Do(request)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ask Codex at %s: %w", hostFromURL(c.baseURL), err)
	}
	defer drain(res)

	if res.StatusCode != http.StatusOK {
		return ChatResponse{}, fmt.Errorf("Codex answered %d: %s", res.StatusCode, excerpt(res.Body))
	}

	content, err := readResponseStream(res.Body)
	if err != nil {
		return ChatResponse{}, err
	}
	return ChatResponse{Content: Clean(content), Provider: c.Name(), ModelID: c.model}, nil
}

func (c *Codex) authorize(request *http.Request, token oauth.Token) {
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	if token.AccountID != "" {
		request.Header.Set("chatgpt-account-id", token.AccountID)
	}
}

// -/ 3/4

// -- 4/4 HELPER · Request shaping and response decoding --

// splitBySystemMessage turns the two messages this product sends into the shape
// the Responses protocol wants: the rules as instructions, the question as
// input.
func splitBySystemMessage(messages []Message) (string, []map[string]any) {
	var instructions string
	input := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == RoleSystem {
			instructions = joinRules(instructions, message.Content)
			continue
		}
		contentType := "input_text"
		if message.Role == RoleAssistant {
			contentType = "output_text"
		}
		input = append(input, map[string]any{
			"type": "message",
			"role": message.Role,
			"content": []map[string]any{
				{"type": contentType, "text": message.Content},
			},
		})
	}
	return instructions, input
}

func joinRules(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "\n\n" + addition
}

// readResponseStream accumulates the answer out of the event stream. The deltas
// are the normal path; the completion event is read too because some answers
// arrive whole in it and never as deltas.
func readResponseStream(body io.Reader) (string, error) {
	var text strings.Builder
	var whole string

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		payload, isData := strings.CutPrefix(line, "data:")
		if !isData {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var event struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Output []struct {
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Response.Error != nil {
			return "", fmt.Errorf("Codex could not answer: %s", event.Response.Error.Message)
		}
		if event.Delta != "" {
			text.WriteString(event.Delta)
		}
		for _, output := range event.Response.Output {
			for _, part := range output.Content {
				if part.Text != "" {
					whole = joinRules(whole, part.Text)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Codex's answer: %w", err)
	}
	if text.Len() > 0 {
		return text.String(), nil
	}
	return whole, nil
}

// -/ 4/4
