package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultMaxTokens is the budget for one answer. The answer is a SELECT, not an
// essay.
const DefaultMaxTokens = 500

// errorBodyBudget is how much of a failed response's body travels into the
// error. Enough to say what the provider complained about, not enough to dump
// an HTML page into the operator's terminal.
const errorBodyBudget = 300

// postJSON is the whole transport these adapters share, avoiding provider SDK
// dependencies.
func postJSON(ctx context.Context, client *http.Client, endpoint string,
	headers map[string]string, body any, into any) error {

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer drain(res)

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("it answered %d: %s", res.StatusCode, excerpt(res.Body))
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(into)
}

// excerpt reads what a failed response had to say, bounded.
func excerpt(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, errorBodyBudget))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// drain closes a response after emptying it, so the connection goes back to the
// pool instead of being thrown away on every call.
func drain(res *http.Response) {
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
	res.Body.Close()
}

func maxTokens(req ChatRequest) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return DefaultMaxTokens
}

// normalizeBaseURL accepts what an operator actually writes: a full URL, a
// host:port, or a bare host.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.Contains(raw, "://") {
		return strings.TrimRight(raw, "/")
	}
	return "http://" + strings.TrimRight(raw, "/")
}

func hostFromURL(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return rawURL
}

func orDefaultClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	// No timeout on the client: the budget is imposed per call by the cascade's
	// context, which is the one that knows whether this is a probe or a query.
	return &http.Client{}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
