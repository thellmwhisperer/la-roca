package vector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaPullAndBatchEmbeddingContract(t *testing.T) {
	var pulled string
	var embedded struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pull":
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			pulled = body.Name
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success"}`))
		case "/api/embed":
			if err := json.NewDecoder(r.Body).Decode(&embedded); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"embeddings":[[1,0,0]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Ollama{BaseURL: server.URL, Client: server.Client()}
	if err := client.Pull(context.Background(), DefaultModel); err != nil {
		t.Fatal(err)
	}
	if pulled != DefaultModel {
		t.Fatalf("pulled %q, want %q", pulled, DefaultModel)
	}
	vectors, err := client.Embed(context.Background(), DefaultModel, []string{DocumentPrefix + "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 || embedded.Model != DefaultModel || embedded.Input[0] != DocumentPrefix+"fixture" {
		t.Fatalf("embed request = %+v, vectors = %+v", embedded, vectors)
	}
}
