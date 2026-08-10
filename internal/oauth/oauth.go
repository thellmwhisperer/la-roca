// Package oauth is the subscription login: the browser flow with PKCE, the
// credential on disk with the permissions of a secret, and the silent renewal.
//
// Why this exists at all is the decision of 2026-08-05: OAuth is a
// must at release because Codex is the most generous plan in tokens and users
// come in with the subscription they already pay for, with no platform key.
//
// Two properties this package is written around, and neither is optional:
//
//   - **A vendor's OAuth flow is fragile and changes with no notice.** So it
//     fails clearly and it fails alone: everything here returns an error the
//     operator can read, and the provider cascade degrades to the next one or to
//     the local floor. The fragility of one vendor never takes down a query.
//   - **The credential is not the operator's key: it is a session.** It is
//     written at the config path with 0600, it never travels to any output and
//     it never lands in the database.
//
// The mechanics (PKCE over a loopback redirect, an identity token that carries
// the account) are the vendor's public flow, the same one third parties like
// OpenCode and Pi implement. What is written here is written from that
// protocol, not copied from them.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// renewalMargin is how early a token is considered expired. A token that is
// valid when the request leaves and dead when it arrives is a failure the
// operator cannot explain.
const renewalMargin = 60 * time.Second

// Token is a subscription session on disk.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ObtainedAt   time.Time `json:"obtained_at,omitempty"`
}

// Expired says whether it has to be renewed before being used. A token with no
// declared expiry is not assumed dead: some vendors do not send one, and
// renewing on every call would be a login loop.
func (t Token) Expired(now time.Time) bool {
	if t.ExpiresAt.IsZero() {
		return false
	}
	return now.Add(renewalMargin).After(t.ExpiresAt)
}

// PKCE is the proof that whoever exchanges the code is whoever asked for it.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE draws a fresh verifier and its S256 challenge.
func NewPKCE() (PKCE, error) {
	verifier, err := randomURLSafe(64)
	if err != nil {
		return PKCE{}, err
	}
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

// NewState draws the value that ties the callback to this flow and to no other.
func NewState() (string, error) { return randomURLSafe(24) }

func randomURLSafe(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("draw a random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Endpoints is a vendor's flow, declared as data so that adding a second
// subscription provider is a table entry and not another package.
type Endpoints struct {
	Authorize string
	Token     string
	ClientID  string
	Scopes    string
	Redirect  string
	// Extra are the vendor's own parameters on the authorization URL.
	Extra map[string]string
}

// CodexPort is the loopback port the vendor's flow redirects back to. It is
// fixed by the vendor, not chosen here: the registered client only accepts this
// callback.
const CodexPort = 1455

// CodexEndpoints is the Codex/OpenAI subscription flow.
func CodexEndpoints() Endpoints {
	return Endpoints{
		Authorize: "https://auth.openai.com/oauth/authorize",
		Token:     "https://auth.openai.com/oauth/token",
		// The public client identifier of the vendor's own CLI. It is not a
		// secret: a public OAuth client has no secret, which is exactly why the
		// flow needs PKCE.
		ClientID: "app_EMoamEEZ73f0CkXaXp7hrann",
		// offline_access is the one that earns the refresh token, and without a
		// refresh token there is no silent renewal and the operator logs in
		// every hour.
		Scopes:   "openid profile email offline_access",
		Redirect: fmt.Sprintf("http://localhost:%d/auth/callback", CodexPort),
		Extra: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
		},
	}
}

// Flow performs a vendor's login.
type Flow struct {
	Endpoints
	Client *http.Client
	// Originator is who is asking, which the vendor logs. It is this product's
	// name: passing somebody else's would be lying about who is connecting.
	Originator string
}

// AuthorizeURL is the address the operator's browser opens.
func (f Flow) AuthorizeURL(pkce PKCE, state string) string {
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.ClientID},
		"redirect_uri":          {f.Redirect},
		"scope":                 {f.Scopes},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	for key, value := range f.Extra {
		query.Set(key, value)
	}
	if f.Originator != "" {
		query.Set("originator", f.Originator)
	}
	return f.Authorize + "?" + query.Encode()
}

// Exchange turns the code the callback brought into a session.
func (f Flow) Exchange(ctx context.Context, code, verifier string) (Token, error) {
	return f.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {f.Redirect},
		"client_id":     {f.ClientID},
		"code_verifier": {verifier},
	}, Token{})
}

// Refresh renews a session without asking the operator for anything.
func (f Flow) Refresh(ctx context.Context, current Token) (Token, error) {
	if current.RefreshToken == "" {
		return Token{}, fmt.Errorf("this session has no refresh token: log in again")
	}
	return f.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {current.RefreshToken},
		"client_id":     {f.ClientID},
	}, current)
}

// token posts the form and reads the vendor's answer, keeping from the previous
// session what the answer does not carry.
func (f Flow) token(ctx context.Context, form url.Values, previous Token) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Token,
		strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := f.client().Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("reach the vendor's identity service: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Token{}, err
	}
	if res.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("the vendor's identity service answered %d: %s",
			res.StatusCode, vendorReason(body))
	}

	var answer struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return Token{}, fmt.Errorf("read the vendor's answer: %w", err)
	}
	if answer.AccessToken == "" {
		return Token{}, fmt.Errorf("the vendor's identity service returned no access token")
	}

	now := time.Now().UTC()
	token := Token{
		AccessToken: answer.AccessToken,
		// A vendor that does not return a new refresh token is not revoking the
		// old one, and throwing it away turns the silent renewal into a login
		// every hour.
		RefreshToken: firstNonEmpty(answer.RefreshToken, previous.RefreshToken),
		IDToken:      firstNonEmpty(answer.IDToken, previous.IDToken),
		ObtainedAt:   now,
	}
	if answer.ExpiresIn > 0 {
		token.ExpiresAt = now.Add(time.Duration(answer.ExpiresIn) * time.Second)
	}
	token.AccountID = firstNonEmpty(accountFromIDToken(token.IDToken), previous.AccountID)
	return token, nil
}

// vendorReason keeps what the vendor complained about, bounded: enough to
// diagnose, not enough to dump a login page into the terminal.
func vendorReason(body []byte) string {
	var envelope struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error != "" {
		if envelope.Description != "" {
			return envelope.Error + ": " + envelope.Description
		}
		return envelope.Error
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

func (f Flow) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	return &http.Client{}
}

// accountFromIDToken reads the account the session belongs to out of the
// identity token's claims. The signature is not verified and it does not need
// to be: this token came straight from the vendor over TLS and it is used to
// address the request, never to grant anything.
func accountFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return ""
	}
	return claims.Auth.AccountID
}

// Store is the credential on disk.
type Store struct{ Path string }

// Save writes the session with the permissions of a secret, creating the
// directory with the permissions of a secret's directory.
func (s Store) Save(token Token) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create the credential directory: %w", err)
	}
	payload, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.Path, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write the credential: %w", err)
	}
	// WriteFile only applies the mode when it creates the file: a credential
	// the operator left world-readable stays that way unless it is tightened
	// here.
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return fmt.Errorf("restrict the credential's permissions: %w", err)
	}
	return nil
}

// Load reads the session.
func (s Store) Load() (Token, error) {
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return Token{}, err
	}
	var token Token
	if err := json.Unmarshal(raw, &token); err != nil {
		return Token{}, fmt.Errorf("read the credential at %s: %w", s.Path, err)
	}
	return token, nil
}

// Exists says whether there is a session on disk.
func (s Store) Exists() bool {
	info, err := os.Stat(s.Path)
	return err == nil && !info.IsDir()
}

// Delete forgets the session. Deleting what is not there is not a failure: the
// end state is the one asked for.
func (s Store) Delete() error {
	err := os.Remove(s.Path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Session is a stored credential that renews itself. It is what an adapter
// holds: it asks for a token and never finds out whether it had to be renewed.
type Session struct {
	Store Store
	Flow  Flow
}

// Token returns a usable access token, renewing and persisting when it has
// expired.
func (s Session) Token(ctx context.Context) (Token, error) {
	token, err := s.Store.Load()
	if err != nil {
		if os.IsNotExist(err) {
			return Token{}, fmt.Errorf("there is no session at %s", s.Store.Path)
		}
		return Token{}, err
	}
	if !token.Expired(time.Now()) {
		return token, nil
	}

	renewed, err := s.refresh(ctx, token)
	if err != nil {
		return Token{}, fmt.Errorf("refresh the expired access token: %w", err)
	}
	return renewed, nil
}

func (s Session) Refresh(ctx context.Context) (Token, error) {
	token, err := s.Store.Load()
	if err != nil {
		return Token{}, err
	}
	return s.refresh(ctx, token)
}

func (s Session) refresh(ctx context.Context, token Token) (Token, error) {
	renewed, err := s.Flow.Refresh(ctx, token)
	if err != nil {
		return Token{}, err
	}
	if err := s.Store.Save(renewed); err != nil {
		return Token{}, err
	}
	return renewed, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
