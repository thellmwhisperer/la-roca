//go:build acceptance

/**
 * @overview Provides the shared live model catalogue/probe fixture. ~50 lines, no public symbols.
 *
 *   READING GUIDE
 *   -------------
 *   1. newOpenAIModelServer exposes catalogue and chat endpoints
 *   2. Callers own server cleanup through their acceptance world
 *
 *   MAIN FLOW
 *   acceptance setup -> live HTTP catalogue -> one-token probe response
 *
 *   PUBLIC API
 *   ----------
 *   None (acceptance-tag test file)
 *
 * @exports
 * @deps httptest; acceptance login fixtures
 */
package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

// -- 1/1 HELPER · shared catalogue and probe server <- START HERE --

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

// -/ 1/1
