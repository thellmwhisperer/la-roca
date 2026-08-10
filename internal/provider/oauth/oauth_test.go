package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPKCEChallengeIsTheS256OfTheVerifier(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatalf("pkce: %v", err)
	}
	sum := sha256.Sum256([]byte(pkce.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pkce.Challenge != want {
		t.Fatalf("challenge %q, want %q", pkce.Challenge, want)
	}
	if strings.ContainsAny(pkce.Challenge, "=+/") {
		t.Fatalf("the challenge is not base64url without padding: %q", pkce.Challenge)
	}
	if len(pkce.Verifier) < 43 {
		t.Fatalf("the verifier is %d characters and RFC 7636 asks for at least 43", len(pkce.Verifier))
	}
}

func TestTwoPKCEsAreNotTheSame(t *testing.T) {
	first, _ := NewPKCE()
	second, _ := NewPKCE()
	if first.Verifier == second.Verifier {
		t.Fatal("two flows sharing a verifier is one flow that can hijack the other")
	}
}

func TestAuthorizeURLCarriesEverythingTheFlowNeeds(t *testing.T) {
	flow := Flow{Endpoints: CodexEndpoints()}
	pkce := PKCE{Verifier: "v", Challenge: "c"}

	raw := flow.AuthorizeURL(pkce, "the-state")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	query := parsed.Query()
	for key, want := range map[string]string{
		"response_type":         "code",
		"code_challenge":        "c",
		"code_challenge_method": "S256",
		"state":                 "the-state",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(query.Get("scope"), "offline_access") {
		t.Errorf("without offline_access there is no refresh token: %q", query.Get("scope"))
	}
	if query.Get("client_id") == "" {
		t.Error("no client_id")
	}
	if query.Get("redirect_uri") == "" {
		t.Error("no redirect_uri")
	}
	if !strings.Contains(raw, "auth.openai.com") {
		t.Errorf("the authorization endpoint is not the vendor's: %s", raw)
	}
}

func TestCodexRedirectIsLoopbackOnly(t *testing.T) {
	redirect := CodexEndpoints().Redirect
	if !strings.HasPrefix(redirect, "http://localhost:") && !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Fatalf("the callback has to come back to this machine: %q", redirect)
	}
}

// tokenServer answers the vendor's token endpoint.
func tokenServer(t *testing.T, handler func(form url.Values) (int, map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		code, body := handler(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func idTokenWith(accountID string) string {
	claims, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
	})
	return "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
}

func TestExchangeTurnsACodeIntoATokenWithItsExpiry(t *testing.T) {
	var seen url.Values
	server := tokenServer(t, func(form url.Values) (int, map[string]any) {
		seen = form
		return http.StatusOK, map[string]any{
			"access_token":  "at",
			"refresh_token": "rt",
			"id_token":      idTokenWith("acct-1"),
			"expires_in":    3600,
		}
	})
	flow := Flow{Endpoints: Endpoints{Token: server.URL, ClientID: "cid", Redirect: "http://localhost:1455/auth/callback"}}

	token, err := flow.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" {
		t.Fatalf("token %+v", token)
	}
	if token.AccountID != "acct-1" {
		t.Fatalf("the account does not travel: %q", token.AccountID)
	}
	if token.ExpiresAt.IsZero() {
		t.Fatal("without an expiry there is no silent renewal")
	}
	if seen.Get("grant_type") != "authorization_code" {
		t.Errorf("grant_type %q", seen.Get("grant_type"))
	}
	if seen.Get("code_verifier") != "the-verifier" {
		t.Errorf("code_verifier %q", seen.Get("code_verifier"))
	}
	if seen.Get("client_id") != "cid" {
		t.Errorf("client_id %q", seen.Get("client_id"))
	}
}

// A vendor that does not return a new refresh token is not revoking the old
// one, and throwing it away turns a silent renewal into a login every hour.
func TestRefreshKeepsTheOldRefreshTokenWhenNoNewOneComes(t *testing.T) {
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		return http.StatusOK, map[string]any{"access_token": "at2", "expires_in": 3600}
	})
	flow := Flow{Endpoints: Endpoints{Token: server.URL, ClientID: "cid"}}

	token, err := flow.Refresh(context.Background(), Token{RefreshToken: "rt", AccountID: "acct-1"})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if token.RefreshToken != "rt" {
		t.Fatalf("it threw away the refresh token: %+v", token)
	}
	if token.AccessToken != "at2" {
		t.Fatalf("access token %q", token.AccessToken)
	}
	if token.AccountID != "acct-1" {
		t.Fatalf("the account got lost in the renewal: %q", token.AccountID)
	}
}

func TestRefreshFailsWithTheVendorsReason(t *testing.T) {
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		return http.StatusBadRequest, map[string]any{"error": "invalid_grant"}
	})
	flow := Flow{Endpoints: Endpoints{Token: server.URL, ClientID: "cid"}}

	_, err := flow.Refresh(context.Background(), Token{RefreshToken: "rt"})
	if err == nil {
		t.Fatal("a rejected renewal is a failure")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("the error does not carry the vendor's reason: %v", err)
	}
}

// The credential is a session of another product living on the operator's disk.
// It is written with the permissions of a secret or it is not written.
func TestTheStoreWritesTheCredentialUnreadableToAnybodyElse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	store := Store{Path: filepath.Join(dir, "codex.json")}

	if err := store.Save(Token{AccessToken: "at", RefreshToken: "rt"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the credential file is %o and it has to be 600", mode)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("the credential directory is %o and it has to be 700", mode)
	}
}

func TestTheStoreTightensAPreExistingCredentialDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: filepath.Join(dir, "codex.json")}
	if err := store.Save(Token{AccessToken: "at"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory info = %v, err=%v; want mode 0700", info, err)
	}
}

func TestTheStoreRoundTripsAndForgets(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "codex.json")}
	if store.Exists() {
		t.Fatal("it does not exist yet")
	}

	saved := Token{AccessToken: "at", RefreshToken: "rt", AccountID: "acct",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second)}
	if err := store.Save(saved); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !store.Exists() {
		t.Fatal("it was saved and does not exist")
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.AccessToken != saved.AccessToken || loaded.RefreshToken != saved.RefreshToken ||
		loaded.AccountID != saved.AccountID || !loaded.ExpiresAt.Equal(saved.ExpiresAt) {
		t.Fatalf("round trip: %+v vs %+v", loaded, saved)
	}

	if err := store.Delete(); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if store.Exists() {
		t.Fatal("it was deleted and it still exists")
	}
	if err := store.Delete(); err != nil {
		t.Fatalf("deleting what is not there is not a failure: %v", err)
	}
}

// A file the operator left with loose permissions is tightened when it is
// rewritten: converging is worth more than complaining.
func TestSavingOverALoosePermissionFileTightensIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store := Store{Path: path}
	if err := store.Save(Token{AccessToken: "at"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, _ := os.Stat(path)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode %o", mode)
	}
}

func TestExpiredLeavesAMarginSoTheTokenNeverDiesMidRequest(t *testing.T) {
	now := time.Now()
	if (Token{ExpiresAt: now.Add(2 * time.Hour)}).Expired(now) {
		t.Error("two hours out is not expired")
	}
	if !(Token{ExpiresAt: now.Add(10 * time.Second)}).Expired(now) {
		t.Error("ten seconds out has to be renewed before being used")
	}
	if !(Token{ExpiresAt: now.Add(-time.Second)}).Expired(now) {
		t.Error("already past is expired")
	}
	if (Token{AccessToken: "at"}).Expired(now) {
		t.Error("a token with no declared expiry is not assumed dead")
	}
}

// The renewal is automatic and silent: the operator logs in once.
func TestSessionRenewsOnItsOwnAndPersistsTheResult(t *testing.T) {
	var refreshes int
	server := tokenServer(t, func(form url.Values) (int, map[string]any) {
		if form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type %q", form.Get("grant_type"))
		}
		refreshes++
		return http.StatusOK, map[string]any{"access_token": "fresh", "expires_in": 3600}
	})
	store := Store{Path: filepath.Join(t.TempDir(), "codex.json")}
	store.Save(Token{AccessToken: "stale", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Minute)})

	session := Session{Store: store, Flow: Flow{Endpoints: Endpoints{Token: server.URL, ClientID: "cid"}}}

	token, err := session.Token(context.Background())
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if token.AccessToken != "fresh" {
		t.Fatalf("it did not renew: %+v", token)
	}
	persisted, _ := store.Load()
	if persisted.AccessToken != "fresh" {
		t.Fatalf("the renewal was not persisted: %+v", persisted)
	}

	// A second call with the token already fresh does not go back to the vendor.
	if _, err := session.Token(context.Background()); err != nil {
		t.Fatalf("token: %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("it renewed %d times and once was enough", refreshes)
	}
}

func TestConcurrentSessionsRefreshOneRotatingTokenOnce(t *testing.T) {
	var mu sync.Mutex
	refreshes := 0
	server := tokenServer(t, func(form url.Values) (int, map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		refreshes++
		if refreshes > 1 || form.Get("refresh_token") != "rt-once" {
			return http.StatusBadRequest, map[string]any{"error": "invalid_grant"}
		}
		return http.StatusOK, map[string]any{
			"access_token": "fresh", "refresh_token": "rt-next", "expires_in": 3600,
		}
	})
	store := Store{Path: filepath.Join(t.TempDir(), "credentials", "codex.json")}
	if err := store.Save(Token{AccessToken: "stale", RefreshToken: "rt-once", ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	session := Session{Store: store, Flow: Flow{Endpoints: Endpoints{Token: server.URL, ClientID: "cid"}}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			token, err := session.Token(context.Background())
			if err == nil && token.AccessToken != "fresh" {
				err = fmt.Errorf("access token = %q", token.AccessToken)
			}
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent token call: %v", err)
		}
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
}

func TestSeparateProcessesShareTheOAuthRenewalLock(t *testing.T) {
	var mu sync.Mutex
	refreshes := 0
	server := tokenServer(t, func(url.Values) (int, map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		refreshes++
		time.Sleep(100 * time.Millisecond)
		if refreshes > 1 {
			return http.StatusBadRequest, map[string]any{"error": "invalid_grant"}
		}
		return http.StatusOK, map[string]any{
			"access_token": "fresh", "refresh_token": "rt-next", "expires_in": 3600,
		}
	})
	path := filepath.Join(t.TempDir(), "credentials", "codex.json")
	if err := (Store{Path: path}).Save(Token{
		AccessToken: "stale", RefreshToken: "rt-once", ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 2)
	outputs := make([]bytes.Buffer, 2)
	for i := range commands {
		commands[i] = exec.Command(os.Args[0], "-test.run=^TestOAuthRenewalProcess$")
		commands[i].Env = append(os.Environ(),
			"ROCA_OAUTH_RENEWAL_CHILD=1", "ROCA_OAUTH_TOKEN_PATH="+path, "ROCA_OAUTH_TOKEN_URL="+server.URL)
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("renewal process %d: %v\n%s", i, err, outputs[i].String())
		}
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
}

func TestOAuthRenewalProcess(t *testing.T) {
	if os.Getenv("ROCA_OAUTH_RENEWAL_CHILD") != "1" {
		return
	}
	session := Session{
		Store: Store{Path: os.Getenv("ROCA_OAUTH_TOKEN_PATH")},
		Flow:  Flow{Endpoints: Endpoints{Token: os.Getenv("ROCA_OAUTH_TOKEN_URL"), ClientID: "cid"}},
	}
	token, err := session.Token(context.Background())
	if err != nil || token.AccessToken != "fresh" {
		t.Fatalf("token = %+v, err=%v", token, err)
	}
}

func TestSessionWithoutACredentialSaysSoWithoutInventingOne(t *testing.T) {
	session := Session{Store: Store{Path: filepath.Join(t.TempDir(), "codex.json")}}
	if _, err := session.Token(context.Background()); err == nil {
		t.Fatal("with no credential there is no token")
	}
}

func TestAccountIDIsReadFromTheIdentityToken(t *testing.T) {
	if got := accountFromIDToken(idTokenWith("acct-42")); got != "acct-42" {
		t.Fatalf("account %q", got)
	}
	if got := accountFromIDToken("not a jwt"); got != "" {
		t.Fatalf("a malformed token is not an account: %q", got)
	}
	if got := accountFromIDToken(""); got != "" {
		t.Fatalf("account %q", got)
	}
}
