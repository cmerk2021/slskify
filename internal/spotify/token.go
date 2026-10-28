package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TokenSource supplies bearer tokens for the Web API. Implementations must
// be safe for concurrent use. Client Credentials is the only v1 source;
// Authorization Code + PKCE can be added later behind this interface
// without touching Client.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
	// Invalidate drops any cached token (called after a 401).
	Invalidate()
}

// DefaultAccountsURL is the Spotify accounts service base URL.
const DefaultAccountsURL = "https://accounts.spotify.com"

// ClientCredentials implements the OAuth2 Client Credentials flow with
// caching and early refresh.
type ClientCredentials struct {
	ClientID     string
	ClientSecret string
	AccountsURL  string
	HTTP         *http.Client
	// RefreshSkew refreshes the token this long before it actually expires.
	RefreshSkew time.Duration
	now         func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewClientCredentials creates a token source for the given app credentials.
func NewClientCredentials(id, secret string) *ClientCredentials {
	return &ClientCredentials{ClientID: id, ClientSecret: secret}
}

func (c *ClientCredentials) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Invalidate implements TokenSource.
func (c *ClientCredentials) Invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

// Token implements TokenSource.
func (c *ClientCredentials) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	skew := c.RefreshSkew
	if skew == 0 {
		skew = 60 * time.Second
	}
	if c.token != "" && c.clock().Add(skew).Before(c.expires) {
		return c.token, nil
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return "", &Error{Kind: ErrKindAuth, Message: "Spotify client ID and secret are not configured", Hint: "run `slskify setup`"}
	}
	base := c.AccountsURL
	if base == "" {
		base = DefaultAccountsURL
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", &Error{Kind: ErrKindNetwork, Message: "could not reach the Spotify accounts service", Cause: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		return "", &Error{Kind: ErrKindAuth, Status: resp.StatusCode,
			Message: "Spotify rejected the client ID/secret",
			Hint:    "check the credentials of your app at https://developer.spotify.com/dashboard"}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &Error{Kind: ErrKindOther, Status: resp.StatusCode, Message: fmt.Sprintf("Spotify token request failed (HTTP %d)", resp.StatusCode)}
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("unexpected Spotify token response")
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600
	}
	c.token = tok.AccessToken
	c.expires = c.clock().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return c.token, nil
}
