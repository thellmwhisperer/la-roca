//go:build acceptance

package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

func newOpenAIModelServer(model string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/models":
			_ = json.NewEncoder(out).Encode(map[string]any{"data": []any{map[string]any{"id": model}}})
		case "/chat/completions":
			_ = json.NewEncoder(out).Encode(map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": "x"},
			}}})
		default:
			http.NotFound(out, request)
		}
	}))
}
