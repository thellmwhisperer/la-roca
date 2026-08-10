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

func ollamaServer(t *testing.T, models []string, answer string) (*httptest.Server, *[]string) {
	t.Helper()
	var hits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/api/tags":
			list := make([]map[string]any, 0, len(models))
			for _, m := range models {
				list = append(list, map[string]any{"name": m, "model": m})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": list})
		case "/api/chat":
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)
			if stream, _ := req["stream"].(bool); stream {
				t.Errorf("La Roca does not need streaming and asked for it: %s", body)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"role": "assistant", "content": answer},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestOllamaIsReadyWhenItAnswersAndCarriesTheModel(t *testing.T) {
	server, _ := ollamaServer(t, []string{"qwen3.5:4b", "gemma4:12b"}, "")
	ollama := NewOllama(OllamaConfig{BaseURL: server.URL, Model: "qwen3.5:4b"})

	readiness := ollama.Ready(context.Background())
	if !readiness.Ready {
		t.Fatalf("it should be ready: %+v", readiness)
	}
	if readiness.ModelID != "qwen3.5:4b" {
		t.Fatalf("model %q", readiness.ModelID)
	}
}

func TestOllamaWithoutTheModelDownloadedNamesTheExactCommand(t *testing.T) {
	server, _ := ollamaServer(t, []string{"gemma4:12b"}, "")
	ollama := NewOllama(OllamaConfig{BaseURL: server.URL, Model: "qwen3.5:4b"})

	readiness := ollama.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("the model is not downloaded and it says it is ready")
	}
	if !strings.Contains(readiness.Action, "ollama pull qwen3.5:4b") {
		t.Fatalf("the action does not name the exact command: %q", readiness.Action)
	}
	if !strings.Contains(readiness.Reason, "qwen3.5:4b") {
		t.Fatalf("the reason does not name the model: %q", readiness.Reason)
	}
}

func TestOllamaThatDoesNotAnswerNamesHowToStartIt(t *testing.T) {
	// A port nobody is listening on: the local model is not up.
	ollama := NewOllama(OllamaConfig{BaseURL: "http://127.0.0.1:1", Model: "qwen3.5:4b"})

	readiness := ollama.Ready(context.Background())
	if readiness.Ready {
		t.Fatal("nothing is listening and it says it is ready")
	}
	if !strings.Contains(readiness.Action, "ollama serve") {
		t.Fatalf("the action does not name how to start it: %q", readiness.Action)
	}
	if !strings.Contains(readiness.Reason, "127.0.0.1:1") {
		t.Fatalf("the reason does not name where it looked: %q", readiness.Reason)
	}
}

func TestOllamaChatReturnsTheCleanedContent(t *testing.T) {
	server, hits := ollamaServer(t, []string{"qwen3.5:4b"},
		"<think>counting</think>\n```sql\nSELECT count(*) FROM memories LIMIT 1\n```")
	ollama := NewOllama(OllamaConfig{BaseURL: server.URL, Model: "qwen3.5:4b"})

	res, err := ollama.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "how many memories are there"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "SELECT count(*) FROM memories LIMIT 1" {
		t.Fatalf("content %q", res.Content)
	}
	if res.Provider != NameOllama || res.ModelID != "qwen3.5:4b" {
		t.Fatalf("the provenance does not travel: %+v", res)
	}
	if !contains(*hits, "/api/chat") {
		t.Fatalf("it did not call the chat endpoint: %v", *hits)
	}
}

func TestOllamaDefaultsToTheModelTheProductShipsWith(t *testing.T) {
	if got := NewOllama(OllamaConfig{}).ModelID(); got != DefaultOllamaModel {
		t.Fatalf("model %q", got)
	}
	if DefaultOllamaModel != "qwen3.5:4b" {
		t.Fatalf("the default model is %q and the measured floor is qwen3.5:4b", DefaultOllamaModel)
	}
}

func TestOllamaBaseURLAcceptsAHostWithoutAScheme(t *testing.T) {
	ollama := NewOllama(OllamaConfig{BaseURL: "localhost:11434"})
	if got := ollama.BaseURL(); got != "http://localhost:11434" {
		t.Fatalf("base url %q", got)
	}
}

// `roca models` lists every model the local runtime has pulled, not only the
// one configured as the floor. An empty runtime answers ready with no models.
func TestOllamaModelsListsTheDownloadedModels(t *testing.T) {
	server, _ := ollamaServer(t, []string{"qwen3.5:4b", "gemma4:12b"}, "")
	ollama := NewOllama(OllamaConfig{BaseURL: server.URL, Model: "qwen3.5:4b"})

	report := ollama.Models(context.Background())
	if !report.Ready {
		t.Fatalf("a running Ollama is ready to list: %+v", report)
	}
	if got := strings.Join(report.Models, ","); got != "qwen3.5:4b,gemma4:12b" {
		t.Fatalf("models = %q", got)
	}
}

// Nothing listening is the same no as Ready, so `roca models` keeps going past a
// machine with no local runtime instead of failing the whole command.
func TestOllamaModelsNamesTheReasonWhenNothingAnswers(t *testing.T) {
	ollama := NewOllama(OllamaConfig{BaseURL: "http://127.0.0.1:1", Model: "qwen3.5:4b"})
	report := ollama.Models(context.Background())
	if report.Ready || len(report.Models) != 0 {
		t.Fatalf("nothing listening must not be ready with a list: %+v", report)
	}
	if !strings.Contains(report.Reason, "127.0.0.1:1") {
		t.Fatalf("the reason does not name where it looked: %q", report.Reason)
	}
}

func TestOllamaChatOnAServerThatFailsSaysSoWithoutATraceback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model runner crashed", http.StatusInternalServerError)
	}))
	defer server.Close()
	ollama := NewOllama(OllamaConfig{BaseURL: server.URL, Model: "qwen3.5:4b"})

	_, err := ollama.Chat(context.Background(), ChatRequest{})
	if err == nil {
		t.Fatal("a 500 is a failure")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("the error does not carry the status: %v", err)
	}
}

func contains(values []string, wanted string) bool {
	for _, v := range values {
		if v == wanted {
			return true
		}
	}
	return false
}
