package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TokenSet is the result of a code exchange or a refresh.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// Exchange trades an authorization code and PKCE verifier for tokens.
func (c *Client) Exchange(ctx context.Context, code, verifier string) (TokenSet, error) {
	return c.token(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {c.cfg.ClientID},
		"code_verifier": {verifier},
	})
}

// Refresh obtains a new access token. EVE may rotate the refresh token: the
// returned one is the one to persist; the old one is kept only when the
// response omits it.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	ts, err := c.token(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.cfg.ClientID},
	})
	if err != nil {
		return TokenSet{}, err
	}
	if ts.RefreshToken == "" {
		ts.RefreshToken = refreshToken
	}
	return ts, nil
}

func (c *Client) token(ctx context.Context, form url.Values) (TokenSet, error) {
	body, err := c.postForm(ctx, "/v2/oauth/token", form)
	if err != nil {
		return TokenSet{}, err
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return TokenSet{}, fmt.Errorf("sso: decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return TokenSet{}, errors.New("sso: token response has no access_token")
	}
	return TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresIn:    time.Duration(tr.ExpiresIn) * time.Second,
	}, nil
}

// Revoke invalidates a refresh token at EVE SSO.
//
// UNVERIFIED: the revocation parameters for public (secret-less) clients are
// not confirmed against the real service. This follows RFC 7009 (token,
// token_type_hint) plus client_id. Kept isolated so it is easy to adjust.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	_, err := c.postForm(ctx, "/v2/oauth/revoke", url.Values{
		"token":           {refreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {c.cfg.ClientID},
	})
	return err
}

// postForm POSTs a form without any Authorization header and returns the
// body of a 200 response.
func (c *Client) postForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.LoginBaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sso: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("sso: read %s response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso: POST %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
