package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// DefaultOllamaModel is the local floor used to validate query behavior.
const DefaultOllamaModel = "qwen3.5:4b"

// DefaultOllamaBaseURL is where Ollama listens with no configuration.
const DefaultOllamaBaseURL = "http://localhost:11434"

// OllamaConfig is what an operator can retune about the local floor.
type OllamaConfig struct {
	BaseURL string
	Model   string
	// KeepAlive is how long Ollama keeps the model loaded. Empty leaves its
	// default: loading a 4B model costs seconds and paying for it on every
	// query is what makes the local path feel broken.
	KeepAlive string
	// Think asks the model to reason before answering. It is off unless the
	// operator turns it on, and the reason is measured: an interpretation on
	// qwen3.5 with thinking took minutes where the same one without it took
	// seconds. The API field is the only switch that works on that family; a
	// /no_think in the prompt does nothing.
	Think  bool
	Client *http.Client
}

// Ollama is the local floor: no authentication, no network beyond localhost, and
// available on every supported platform. That is why the default order ends
// here.
type Ollama struct {
	baseURL   string
	model     string
	keepAlive string
	think     bool
	client    *http.Client
}

// NewOllama builds the local floor adapter.
func NewOllama(cfg OllamaConfig) *Ollama {
	return &Ollama{
		baseURL:   normalizeBaseURL(firstNonEmpty(cfg.BaseURL, DefaultOllamaBaseURL)),
		model:     firstNonEmpty(cfg.Model, DefaultOllamaModel),
		keepAlive: cfg.KeepAlive,
		think:     cfg.Think,
		client:    orDefaultClient(cfg.Client),
	}
}

// Name is the normalized name the configuration and the answer's provenance
// use.
func (o *Ollama) Name() string { return NameOllama }

// ModelID is the model that is going to answer.
func (o *Ollama) ModelID() string { return o.model }

// BaseURL is where it talks to Ollama. It is reported by doctor and it never
// carries authentication state, because this provider has none.
func (o *Ollama) BaseURL() string { return o.baseURL }

// ollamaTag is one entry of the local runtime's model list, kept whole so Ready
// can match the configured model against either spelling and Models can list the
// name the operator reads.
type ollamaTag struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// tags reads the local runtime's model list. It is the shared half of Ready
// (which asks "is the configured model among them?") and Models (which lists
// them all for `roca models`). A failure to even read the list comes back as a
// Readiness whose Reason is set; the caller decides the remedy.
func (o *Ollama) tags(ctx context.Context) ([]ollamaTag, Readiness) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, Readiness{Reason: err.Error()}
	}
	res, err := o.client.Do(req)
	if err != nil {
		return nil, Readiness{Reason: fmt.Sprintf("Ollama does not answer at %s", strings.TrimPrefix(o.baseURL, "http://"))}
	}
	defer drain(res)
	if res.StatusCode != http.StatusOK {
		return nil, Readiness{Reason: fmt.Sprintf("Ollama at %s answered %d", o.baseURL, res.StatusCode)}
	}
	var body struct {
		Models []ollamaTag `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, Readiness{Reason: fmt.Sprintf("Ollama at %s answered something that is not its model list", o.baseURL)}
	}
	return body.Models, Readiness{}
}

// Ready asks Ollama which models it has. Two different noes, because the
// remedies are different: nothing listening is `ollama serve`, and a model that
// is not there is `ollama pull`.
func (o *Ollama) Ready(ctx context.Context) Readiness {
	tags, fail := o.tags(ctx)
	if fail.Reason != "" {
		return Readiness{Reason: fail.Reason, Action: o.installAction()}
	}
	for _, tag := range tags {
		if tag.Name == o.model || tag.Model == o.model {
			return Readiness{Ready: true, ModelID: o.model}
		}
	}
	return Readiness{
		ModelID: o.model,
		Reason:  fmt.Sprintf("Ollama is running but does not have the model %s", o.model),
		Action:  fmt.Sprintf("download it with `ollama pull %s`", o.model),
	}
}

// Models lists every model the local runtime has pulled. It shares the read
// with Ready: a running Ollama with nothing pulled is ready with an empty list,
// which is the honest answer for `roca models`.
func (o *Ollama) Models(ctx context.Context) ModelReport {
	tags, fail := o.tags(ctx)
	if fail.Reason != "" {
		return ModelReport{Reason: fail.Reason}
	}
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag.Name != "" {
			names = append(names, tag.Name)
		}
	}
	return ModelReport{Ready: true, Models: names}
}

// installAction is the remedy when there is no Ollama answering. It names the
// exact command and, in front of it, where to get it, because an operator who
// has never installed it cannot run `ollama serve` either.
func (o *Ollama) installAction() string {
	return "start the local model with `ollama serve` " +
		"(if you do not have it, install it from https://ollama.com/download)"
}

// Chat asks Ollama for a completion over its native API. No streaming: this
// product asks for one SQL statement and reads it whole, and a stream would only
// add a parser.
func (o *Ollama) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	return o.chat(ctx, req, false, nil)
}

// ChatStream asks Ollama for its newline-delimited response and forwards each
// content chunk while retaining the complete answer.
func (o *Ollama) ChatStream(ctx context.Context, req ChatRequest,
	onDelta func(string)) (ChatResponse, error) {
	return o.chat(ctx, req, true, onDelta)
}

func (o *Ollama) chat(ctx context.Context, req ChatRequest, stream bool,
	onDelta func(string)) (ChatResponse, error) {
	body := map[string]any{
		"model":    o.model,
		"messages": req.Messages,
		"stream":   stream,
		// The thinking of a reasoning model is neither the SQL nor the summary
		// that is asked of it, and paying tokens to generate it and then throw it
		// away is paying twice. It is also the difference between a local
		// interpretation that answers in seconds and one that answers in minutes,
		// and on qwen3.5 this field is the only switch that turns it off.
		"think":   o.think,
		"options": ollamaOptions(req),
	}
	if o.keepAlive != "" {
		body["keep_alive"] = o.keepAlive
	}
	if stream {
		return o.stream(ctx, body, onDelta)
	}

	var answer struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := postJSON(ctx, o.client, o.baseURL+"/api/chat", nil, body, &answer); err != nil {
		return ChatResponse{}, fmt.Errorf("ask Ollama at %s: %w", o.baseURL, err)
	}
	return ChatResponse{
		Content:  answer.Message.Content,
		Provider: o.Name(),
		ModelID:  o.model,
	}, nil
}

func (o *Ollama) stream(ctx context.Context, body map[string]any,
	onDelta func(string)) (ChatResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat",
		bytes.NewReader(raw))
	if err != nil {
		return ChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := o.client.Do(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ask Ollama at %s: %w", o.baseURL, err)
	}
	defer drain(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("ask Ollama at %s: it answered %d: %s",
			o.baseURL, res.StatusCode, excerpt(res.Body))
	}
	var content strings.Builder
	decoder := json.NewDecoder(res.Body)
	for decoder.More() {
		var chunk struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			return ChatResponse{}, fmt.Errorf("read Ollama's answer: %w", err)
		}
		content.WriteString(chunk.Message.Content)
		if onDelta != nil && chunk.Message.Content != "" {
			onDelta(chunk.Message.Content)
		}
	}
	return ChatResponse{Content: content.String(), Provider: o.Name(), ModelID: o.model}, nil
}

func ollamaOptions(req ChatRequest) map[string]any {
	options := map[string]any{
		// Zero temperature: the same question has to compile to the same SQL,
		// or repeatable query tests measure noise.
		"temperature": 0,
	}
	// num_predict is optional on Ollama (unset means unlimited). A default
	// of 500 used to be sent on every call; only the readiness probe sets
	// MaxTokens, and only then do we send the field.
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	return options
}
