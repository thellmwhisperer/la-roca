package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultModel   = "nomic-embed-text-v2"
	DocumentPrefix = "search_document: "
	QueryPrefix    = "search_query: "
	defaultOllama  = "http://127.0.0.1:11434"
)

type Embedder interface {
	Pull(context.Context, string) error
	Embed(context.Context, string, []string) ([][]float32, error)
}

type Ollama struct {
	BaseURL string
	Client  *http.Client
}

func (o Ollama) Pull(ctx context.Context, model string) error {
	response, err := o.post(ctx, "/api/pull", map[string]any{"name": model, "stream": false})
	if err != nil {
		return fmt.Errorf("download Ollama model %s: %w", model, err)
	}
	defer response.Close()
	decoder := json.NewDecoder(response)
	for {
		var event struct {
			Error string `json:"error"`
		}
		if err := decoder.Decode(&event); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return fmt.Errorf("read Ollama model download: %w", err)
		}
		if event.Error != "" {
			return errors.New(event.Error)
		}
	}
}

func (o Ollama) Embed(ctx context.Context, model string, input []string) ([][]float32, error) {
	response, err := o.post(ctx, "/api/embed", map[string]any{
		"model": model, "input": input, "keep_alive": "2h",
	})
	if err != nil {
		return nil, fmt.Errorf("embed with Ollama model %s: %w", model, err)
	}
	defer response.Close()
	var result struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(response).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode Ollama embeddings: %w", err)
	}
	if result.Error != "" {
		return nil, errors.New(result.Error)
	}
	if len(result.Embeddings) != len(input) {
		return nil, fmt.Errorf("Ollama returned %d embeddings for %d inputs", len(result.Embeddings), len(input))
	}
	return result.Embeddings, nil
}

func (o Ollama) post(ctx context.Context, path string, body any) (io.ReadCloser, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(o.BaseURL), "/")
	if base == "" {
		base = defaultOllama
	} else if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("%s: %s", response.Status, strings.TrimSpace(string(message)))
	}
	return response.Body, nil
}
