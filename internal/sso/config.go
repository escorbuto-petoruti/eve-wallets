// Package sso implements EVE SSO v2: Authorization Code with PKCE (public
// client, no secret), token exchange/refresh/revoke and JWT validation.
package sso

import (
	"net/http"
	"strings"
	"time"
)

const (
	defaultLoginBaseURL = "https://login.eveonline.com"
	// eveAudience is the literal audience every EVE access token must carry.
	eveAudience = "EVE Online"
)

// Config configures a Client.
type Config struct {
	ClientID    string
	RedirectURL string
	Scopes      []string
	// LoginBaseURL defaults to https://login.eveonline.com; tests override it.
	LoginBaseURL string
	// HTTPClient defaults to a client with a 15s timeout.
	HTTPClient *http.Client
	// AllowedIssuers defaults to both documented EVE issuer spellings.
	AllowedIssuers []string
}

// Client talks to EVE SSO.
type Client struct {
	cfg  Config
	jwks *jwksCache
}

// NewClient applies defaults to cfg and returns a Client.
func NewClient(cfg Config) *Client {
	if cfg.LoginBaseURL == "" {
		cfg.LoginBaseURL = defaultLoginBaseURL
	}
	cfg.LoginBaseURL = strings.TrimRight(cfg.LoginBaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if len(cfg.AllowedIssuers) == 0 {
		cfg.AllowedIssuers = []string{"login.eveonline.com", "https://login.eveonline.com"}
	}
	c := &Client{cfg: cfg}
	c.jwks = &jwksCache{url: cfg.LoginBaseURL + "/oauth/jwks", http: cfg.HTTPClient, minRefetch: defaultMinRefetch, minRetryAfterFailure: defaultMinRetryAfterFailure}
	return c
}
