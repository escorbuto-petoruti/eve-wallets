package sso

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
)

// randomString returns n random bytes encoded as base64url without padding.
func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewVerifier returns a PKCE code verifier (43 characters).
func NewVerifier() (string, error) { return randomString(32) }

// NewState returns an unguessable OAuth state value.
func NewState() (string, error) { return randomString(32) }

// Challenge derives the S256 code challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthURL builds the authorization URL the user must open in a browser.
func (c *Client) AuthURL(state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.cfg.ClientID},
		"redirect_uri":          {c.cfg.RedirectURL},
		"scope":                 {strings.Join(c.cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	return c.cfg.LoginBaseURL + "/v2/oauth/authorize?" + q.Encode()
}
