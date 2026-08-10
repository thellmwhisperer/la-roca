package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// DefaultOllamaModel is the local floor's model. It is the one the Mac mini
// battery measured (TECH-SPEC 1.4) and the one the SQL repairs, when they are
// built, will be justified against.
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
	Client    *http.Client
}

// Ollama is the local floor: no credential, no network beyond localhost, and
// available on every supported platform. That is why the default order ends
// here.
type Ollama struct {
	baseURL   string
	model     string
	keepAlive string
	client    *http.Client
}

// NewOllama builds the local floor adapter.
func NewOllama(cfg OllamaConfig) *Ollama {
	return &Ollama{
		baseURL:   normalizeBaseURL(firstNonEmpty(cfg.BaseURL, DefaultOllamaBaseURL)),
		model:     firstNonEmpty(cfg.Model, DefaultOllamaModel),
		keepAlive: cfg.KeepAlive,
		client:    orDefaultClient(cfg.Client),
	}
}

// Name is the normalized name the configuration and the answer's provenance
// use.
func (o *Ollama) Name() string { return NameOllama }

// ModelID is the model that is going to answer.
func (o *Ollama) ModelID() string { return o.model }

// BaseURL is where it talks to Ollama. It is reported by doctor and it never
// carries a credential, because this provider has none.
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
	body := map[string]any{
		"model":    o.model,
		"messages": req.Messages,
		"stream":   false,
		// The thinking of a reasoning model is not part of an SQL statement, and
		// paying tokens to generate it and then throwing it away is paying twice.
		"think": false,
		"options": map[string]any{
			"num_predict": maxTokens(req),
			// Zero temperature: the same question has to compile to the same SQL,
			// or the golden bench measures noise.
			"temperature": 0,
		},
	}
	if o.keepAlive != "" {
		body["keep_alive"] = o.keepAlive
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
