package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// freePort picks a port nobody is using, so the test never fights the real
// login for the vendor's fixed one.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func loginFlow(t *testing.T, tokenURL string) Flow {
	t.Helper()
	endpoints := CodexEndpoints()
	endpoints.Token = tokenURL
	endpoints.Redirect = fmt.Sprintf("http://127.0.0.1:%d/auth/callback", freePort(t))
	return Flow{Endpoints: endpoints, Originator: "roca"}
}

// The fake server's secrets are distinctive on purpose: a leak assertion over a
// two-letter token cannot tell a real leak from the word "paste".
const (
	accessSentinel  = "sentinel-access-9f3c1e"
	refreshSentinel = "sentinel-refresh-7b1d40"
)

func TestLoginWaitsForTheCallbackAndExchangesTheCode(t *testing.T) {
	server := tokenServer(t, func(form url.Values) (int, map[string]any) {
		if form.Get("code") != "the-code" {
			t.Errorf("code %q", form.Get("code"))
		}
		if form.Get("code_verifier") == "" {
			t.Error("it exchanged without proving PKCE")
		}
		return http.StatusOK, map[string]any{
			"access_token": accessSentinel, "refresh_token": refreshSentinel,
			"id_token": idTokenWith("acct-7"), "expires_in": 3600,
		}
	})

	var printed bytes.Buffer
	flow := loginFlow(t, server.URL)
	token, err := flow.Login(context.Background(), LoginOptions{
		Out: &printed,
		// The browser is the operator's: the test plays it and answers the
		// callback the way a real browser would.
		OpenBrowser: func(raw string) error {
			parsed, err := url.Parse(raw)
			if err != nil {
				return err
			}
			state := parsed.Query().Get("state")
			go func() {
				res, err := http.Get(flow.Redirect + "?code=the-code&state=" + url.QueryEscape(state))
				if err == nil {
					res.Body.Close()
				}
			}()
			return nil
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token.AccessToken != accessSentinel || token.RefreshToken != refreshSentinel ||
		token.AccountID != "acct-7" {
		t.Fatalf("token %+v", token)
	}
	// The operator has to be able to finish the login with no browser: the URL
	// is always printed.
	got := printed.String()
	if !strings.Contains(got, "auth.openai.com") {
		t.Fatalf("it did not print the address to open: %q", got)
	}
	// Each secret is checked on its own. The old assertion joined three
	// substrings with AND, so it only fired when all three appeared, and its
	// two-letter fixtures ("at", "rt") matched ordinary prose besides.
	for _, secret := range []string{accessSentinel, refreshSentinel} {
		if strings.Contains(got, secret) {
			t.Fatalf("the credential %q leaked to the output: %q", secret, got)
		}
	}
	// The OAuth narrative is a few fixed lines before anything opens. Pinning
	// them here keeps the wording terse and stops a later edit from dropping
	// the waiting line or turning them into a wall of text.
	for _, want := range []string{
		LoginNarrative,
		LoginWaiting,
		"browser will open",
		"access token, never your password",
		"roca logout codex",
		"Ctrl+C",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("narrative output missing %q:\n%s", want, got)
		}
	}
}

// The narrative has to be on the page before the browser opens: that is the
// whole point of printing it. Measured by capturing Out at the moment Open
// is called, not after Login returns.
func TestLoginPrintsTheNarrativeBeforeOpeningTheBrowser(t *testing.T) {
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		return http.StatusOK, map[string]any{"access_token": "at", "expires_in": 60}
	})
	var atOpen string
	flow := loginFlow(t, server.URL)
	var printed bytes.Buffer
	_, err := flow.Login(context.Background(), LoginOptions{
		Out: &printed,
		OpenBrowser: func(raw string) error {
			atOpen = printed.String()
			parsed, _ := url.Parse(raw)
			go http.Get(flow.Redirect + "?code=c&state=" + url.QueryEscape(parsed.Query().Get("state")))
			return nil
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(atOpen, LoginNarrative) {
		t.Fatalf("browser opened before the narrative was printed: %q", atOpen)
	}
}

// A callback with somebody else's state is not this flow's callback.
func TestLoginRejectsACallbackWithTheWrongState(t *testing.T) {
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		t.Error("it must not exchange anything")
		return http.StatusOK, map[string]any{}
	})

	flow := loginFlow(t, server.URL)
	page := make(chan string, 1)
	_, err := flow.Login(context.Background(), LoginOptions{
		Out: &bytes.Buffer{},
		OpenBrowser: func(string) error {
			go func() {
				res, err := http.Get(flow.Redirect + "?code=c&state=somebody-elses")
				if err == nil {
					body, _ := io.ReadAll(res.Body)
					page <- string(body)
					res.Body.Close()
				}
			}()
			return nil
		},
		Timeout: 3 * time.Second,
	})
	if err == nil {
		t.Fatal("a callback with another state has to be rejected")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Fatalf("the error does not say what went wrong: %v", err)
	}
	if body := <-page; strings.Contains(body, "now connected") {
		t.Fatalf("wrong-state callback received the success page: %q", body)
	}
}

// The vendor cancels by sending back an error, and the operator has to read the
// vendor's reason, not a timeout.
func TestLoginReportsTheErrorTheVendorSendsBack(t *testing.T) {
	flow := loginFlow(t, "http://127.0.0.1:1/token")
	_, err := flow.Login(context.Background(), LoginOptions{
		Out: &bytes.Buffer{},
		OpenBrowser: func(raw string) error {
			parsed, _ := url.Parse(raw)
			state := parsed.Query().Get("state")
			go func() {
				res, err := http.Get(flow.Redirect + "?error=access_denied&state=" + url.QueryEscape(state))
				if err == nil {
					res.Body.Close()
				}
			}()
			return nil
		},
		Timeout: 3 * time.Second,
	})
	if err == nil {
		t.Fatal("a denied login is a failure")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("the error does not carry the vendor's reason: %v", err)
	}
}

// A browser that does not open is not a broken login: the operator opens the
// address by hand.
func TestLoginKeepsGoingWhenTheBrowserDoesNotOpen(t *testing.T) {
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		return http.StatusOK, map[string]any{"access_token": "at", "expires_in": 60}
	})
	flow := loginFlow(t, server.URL)

	var printed bytes.Buffer
	done := make(chan error, 1)
	go func() {
		_, err := flow.Login(context.Background(), LoginOptions{
			Out:         &printed,
			OpenBrowser: func(string) error { return fmt.Errorf("no browser here") },
			Timeout:     5 * time.Second,
		})
		done <- err
	}()

	// The callback server is up even though the browser failed: the address was
	// printed and the operator pastes it.
	deadline := time.Now().Add(3 * time.Second)
	var state string
	for time.Now().Before(deadline) && state == "" {
		if raw := printed.String(); strings.Contains(raw, "state=") {
			if parsed, err := url.Parse(strings.TrimSpace(lastURL(raw))); err == nil {
				state = parsed.Query().Get("state")
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state == "" {
		t.Fatalf("it printed no usable address: %q", printed.String())
	}
	res, err := http.Get(flow.Redirect + "?code=c&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	res.Body.Close()

	if err := <-done; err != nil {
		t.Fatalf("login: %v", err)
	}
}

func TestLoginGivesUpAtItsDeadlineWithoutHangingForever(t *testing.T) {
	flow := loginFlow(t, "http://127.0.0.1:1/token")
	start := time.Now()
	_, err := flow.Login(context.Background(), LoginOptions{
		Out:         &bytes.Buffer{},
		OpenBrowser: func(string) error { return nil },
		Timeout:     150 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("nobody came back and it returned a session")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("it waited %v past its deadline", elapsed)
	}
}

func TestLoginDeadlineAlsoBoundsTheTokenExchange(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	flow := loginFlow(t, server.URL)
	started := time.Now()
	_, err := flow.Login(context.Background(), LoginOptions{
		Out: &bytes.Buffer{},
		OpenBrowser: func(raw string) error {
			parsed, _ := url.Parse(raw)
			state := url.QueryEscape(parsed.Query().Get("state"))
			go func() {
				res, requestErr := http.Get(flow.Redirect + "?code=c&state=" + state)
				if requestErr == nil {
					res.Body.Close()
				}
			}()
			return nil
		},
		Timeout: 150 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("unbounded token exchange returned no error")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("token exchange exceeded the login deadline: %v", time.Since(started))
	}
}

// The callback port is the vendor's and it may be taken by another login in
// flight. Saying so beats a stack trace.
func TestLoginSaysSoWhenTheCallbackPortIsTaken(t *testing.T) {
	port := freePort(t)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("occupy: %v", err)
	}
	defer listener.Close()

	endpoints := CodexEndpoints()
	endpoints.Redirect = fmt.Sprintf("http://127.0.0.1:%d/auth/callback", port)
	flow := Flow{Endpoints: endpoints}

	_, err = flow.Login(context.Background(), LoginOptions{
		Out: &bytes.Buffer{}, OpenBrowser: func(string) error { return nil },
		Timeout: time.Second,
	})
	if err == nil {
		t.Fatal("the port is taken and it says nothing")
	}
	if !strings.Contains(err.Error(), fmt.Sprint(port)) {
		t.Fatalf("the error does not name the port: %v", err)
	}
}

// lastURL pulls the address out of whatever prose surrounds it.
func lastURL(text string) string {
	index := strings.Index(text, "https://")
	if index < 0 {
		return ""
	}
	rest := text[index:]
	if cut := strings.IndexAny(rest, " \n\t"); cut > 0 {
		return rest[:cut]
	}
	return rest
}

var _ = json.Marshal
