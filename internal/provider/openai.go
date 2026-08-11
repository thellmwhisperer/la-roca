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
)

// OpenAIPreset is a provider that speaks the OpenAI-compatible protocol,
// declared as data.
//
// This is the whole reason there is one adapter and not four: DeepSeek, z.ai
// and xAI differ in a URL, a model name and the environment variable their
// credential usually lives in. Writing three clients for that is writing the
// same client three times and maintaining three chances to get it wrong.
type OpenAIPreset struct {
	BaseURL string
	Model   string
	// KeyEnv is where that provider's credential usually lives, so an operator
	// who already exported it does not have to write it down again.
	KeyEnv string
	// EnvAliases are other environment variables this provider's credential
	// answers to, by the model family the operator knows it as: GLM for z.ai,
	// Grok for xAI. They are read alongside KeyEnv so an operator who thinks of
	// the model rather than the vendor does not have to learn the vendor's name.
	EnvAliases []string
	// Label is the provider's name in the operator's language, for the doctor's
	// diagnosis.
	Label string
}

// presets are the frontier providers by key this build knows.
var presets = map[string]OpenAIPreset{
	NameDeepSeek: {
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-chat",
		KeyEnv:  "DEEPSEEK_API_KEY",
		Label:   "DeepSeek",
	},
	NameZAI: {
		BaseURL:    "https://api.z.ai/api/paas/v4",
		Model:      "glm-4.6",
		KeyEnv:     "ZAI_API_KEY",
		EnvAliases: []string{"ROCA_GLM_API_KEY"},
		Label:      "z.ai (GLM)",
	},
	NameXAI: {
		BaseURL:    "https://api.x.ai/v1",
		Model:      "grok-4",
		KeyEnv:     "XAI_API_KEY",
		EnvAliases: []string{"ROCA_GROK_API_KEY"},
		Label:      "xAI (Grok)",
	},
}

// Preset returns the preset of a known provider.
func Preset(name string) (OpenAIPreset, bool) {
	preset, ok := presets[normalize(name)]
	return preset, ok
}

// PresetNames are the presets this build carries, in a stable order.
func PresetNames() []string { return []string{NameDeepSeek, NameZAI, NameXAI} }

// OpenAIConfig is what the configuration says about one OpenAI-compatible
// provider.
type OpenAIConfig struct {
	// Name is what the operator called it in the order. It can be a preset's
	// name or any name of their own for a provider of their own.
	Name string
	// Preset fills in what the operator did not write. Empty means the name is
	// tried as a preset, and if that is not one either, everything has to be
	// declared.
	Preset  string
	BaseURL string
	Model   string
	APIKey  string
	// File is the config file, so that a message about a missing credential
	// names where to write it.
	File   string
	Client *http.Client
}

// OpenAICompatible is the generic adapter for anything that speaks
// `/chat/completions`: the frontier providers by key and any local or remote
// gateway of the operator's.
type OpenAICompatible struct {
	name       string
	label      string
	baseURL    string
	model      string
	apiKey     string
	keyEnv     string
	envAliases []string
	file       string
	client     *http.Client
}

// NewOpenAICompatible builds the adapter, filling in from the preset what the
// operator did not write.
func NewOpenAICompatible(cfg OpenAIConfig) (*OpenAICompatible, error) {
	name := normalize(firstNonEmpty(cfg.Name, cfg.Preset))
	preset, _ := Preset(firstNonEmpty(cfg.Preset, cfg.Name))

	baseURL := firstNonEmpty(cfg.BaseURL, preset.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf(
			"the provider %q declares no base_url and this version has no preset for it: "+
				"write base_url and model in [models.%s]", name, name)
	}
	model := firstNonEmpty(cfg.Model, preset.Model)
	if model == "" {
		return nil, fmt.Errorf(
			"the provider %q declares no model: write model in [models.%s]", name, name)
	}
	return &OpenAICompatible{
		name:       name,
		label:      firstNonEmpty(preset.Label, name),
		baseURL:    normalizeBaseURL(baseURL),
		model:      model,
		apiKey:     cfg.APIKey,
		keyEnv:     preset.KeyEnv,
		envAliases: preset.EnvAliases,
		file:       cfg.File,
		client:     orDefaultClient(cfg.Client),
	}, nil
}

// Name is the normalized name the order and the answer's provenance use.
func (o *OpenAICompatible) Name() string { return o.name }

// ModelID is the model that is going to answer.
func (o *OpenAICompatible) ModelID() string { return o.model }

// BaseURL is where it asks. It never carries the credential: the key travels in
// a header, not in the URL, precisely so that it can be printed.
func (o *OpenAICompatible) BaseURL() string { return o.baseURL }

func (o *OpenAICompatible) HasCredential() bool { return o.apiKey != "" }

// getModels reaches the catalogue endpoint both Ready and Models ask. It is the
// shared half of the two questions over the same endpoint: "can I reach it?"
// (Ready, which reads only the status) and "what do you have?" (Models, which
// reads the body).
func (o *OpenAICompatible) getModels(ctx context.Context) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	return o.client.Do(req)
}

func (o *OpenAICompatible) Ready(ctx context.Context) Readiness {
	if o.apiKey == "" {
		return Readiness{ModelID: o.model, Reason: o.noCredentialReason(), Action: o.credentialAction()}
	}

	res, err := o.getModels(ctx)
	if err != nil {
		return Readiness{
			ModelID: o.model,
			Reason:  unreachable(o.label+" at "+hostFromURL(o.baseURL), err),
			Action:  "check the network, or let the cascade fall to the local floor",
		}
	}
	defer drain(res)

	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return Readiness{Ready: true, ModelID: o.model}
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return Readiness{
			ModelID: o.model,
			Reason:  fmt.Sprintf("%s received HTTP status %d", o.label, res.StatusCode),
			Action:  o.credentialAction(),
		}
	case res.StatusCode >= 500:
		return Readiness{
			ModelID: o.model,
			Reason:  fmt.Sprintf("%s received HTTP status %d", o.label, res.StatusCode),
			Action:  "it is the provider's problem: try again later or use another one in the order",
		}
	case res.StatusCode == http.StatusTooManyRequests:
		return Readiness{ModelID: o.model, Reason: fmt.Sprintf("%s received HTTP status %d", o.label, res.StatusCode),
			Action: "wait and retry, or use another provider in the order"}
	default:
		return Readiness{ModelID: o.model,
			Reason: fmt.Sprintf("%s received HTTP status %d", o.label, res.StatusCode),
			Action: "check the provider endpoint and credential"}
	}
}

// Models lists the catalogue the credential reaches. It shares the request with
// Ready and keeps the body the probe discards: a 2xx is decoded into the model
// ids, anything else is the same not-ready report a failed probe is.
func (o *OpenAICompatible) Models(ctx context.Context) ModelReport {
	if o.apiKey == "" {
		return ModelReport{Reason: o.noCredentialReason()}
	}
	res, err := o.getModels(ctx)
	if err != nil {
		return ModelReport{Reason: unreachable(o.label+" at "+hostFromURL(o.baseURL), err)}
	}
	defer drain(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ModelReport{Reason: fmt.Sprintf("%s received HTTP status %d", o.label, res.StatusCode)}
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return ModelReport{Reason: fmt.Sprintf("%s answered something that is not a model list", o.label)}
	}
	models := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return ModelReport{Ready: true, Models: models}
}

func (o *OpenAICompatible) noCredentialReason() string {
	if o.keyEnv != "" {
		return fmt.Sprintf("there is no credential for %s", o.label)
	}
	return fmt.Sprintf("there is no credential for the provider %q", o.name)
}

func (o *OpenAICompatible) credentialAction() string {
	var action string
	if IsKeyProvider(o.name) {
		action = fmt.Sprintf("log in with `roca login %s`", o.name)
	} else {
		file := o.file
		if file == "" {
			file = "your config.toml"
		}
		action = fmt.Sprintf("write api_key under models.%s in %s", o.name, file)
	}
	return action + o.exportSuffix()
}

// exportSuffix names every environment variable the credential may live in —
// the provider's usual one and any model-family alias (GLM for z.ai, Grok for
// xAI) — so the diagnosis points at each spelling that works, not only the
// vendor's.
func (o *OpenAICompatible) exportSuffix() string {
	var envs []string
	if o.keyEnv != "" {
		envs = append(envs, o.keyEnv)
	}
	envs = append(envs, o.envAliases...)
	if len(envs) == 0 {
		return ""
	}
	return ", or export " + strings.Join(envs, " or ")
}

// Chat asks for a completion over the OpenAI-compatible protocol.
func (o *OpenAICompatible) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body := o.chatBody(req, false)

	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	headers := map[string]string{"Authorization": "Bearer " + o.apiKey}
	if err := postJSON(ctx, o.client, o.baseURL+"/chat/completions", headers, body, &answer); err != nil {
		return ChatResponse{}, fmt.Errorf("ask %s: %w", o.label, err)
	}
	if len(answer.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("%s answered with no choices", o.label)
	}
	return ChatResponse{
		Content:  answer.Choices[0].Message.Content,
		Provider: o.name,
		ModelID:  o.model,
	}, nil
}

// ChatStream reads the standard chat-completions event stream and forwards
// only answer text. The buffered Chat path remains the default for SQL, pipes
// and machine output.
func (o *OpenAICompatible) ChatStream(ctx context.Context, req ChatRequest,
	onDelta func(string)) (ChatResponse, error) {
	raw, err := json.Marshal(o.chatBody(req, true))
	if err != nil {
		return ChatResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ChatResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+o.apiKey)
	res, err := o.client.Do(request)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ask %s: %w", o.label, err)
	}
	defer drain(res)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("ask %s: it answered %d: %s",
			o.label, res.StatusCode, excerpt(res.Body))
	}
	content, err := readChatCompletionStream(res.Body, onDelta)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("read %s's answer: %w", o.label, err)
	}
	return ChatResponse{Content: content, Provider: o.name, ModelID: o.model}, nil
}

func (o *OpenAICompatible) chatBody(req ChatRequest, stream bool) map[string]any {
	return map[string]any{
		"model":      o.model,
		"messages":   req.Messages,
		"max_tokens": maxTokens(req),
		// Zero temperature: the same question has to compile to the same SQL.
		"temperature": 0,
		"stream":      stream,
	}
}

func readChatCompletionStream(body io.Reader, onDelta func(string)) (string, error) {
	var content strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		payload, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Error != nil {
			return "", fmt.Errorf("provider could not answer: %s", event.Error.Message)
		}
		for _, choice := range event.Choices {
			delta := choice.Delta.Content
			content.WriteString(delta)
			if onDelta != nil && delta != "" {
				onDelta(delta)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return content.String(), nil
}
