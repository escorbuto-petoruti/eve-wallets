package sso

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the validated identity carried by an EVE access token.
type Claims struct {
	CharacterID   int64
	CharacterName string
	Scopes        []string
	ExpiresAt     time.Time
}

type eveClaims struct {
	Name  string `json:"name"`
	Scope any    `json:"scp"`
	jwt.RegisteredClaims
}

// Validate verifies an access token (RS256 signature against the SSO JWKS,
// issuer, expiry and audiences) and returns the character identity.
func (c *Client) Validate(ctx context.Context, accessToken string) (Claims, error) {
	var ec eveClaims
	_, err := jwt.ParseWithClaims(accessToken, &ec, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("sso: token has no kid")
		}
		return c.jwks.key(ctx, kid)
	},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("sso: invalid token: %w", err)
	}
	if !slices.Contains(c.cfg.AllowedIssuers, ec.Issuer) {
		return Claims{}, fmt.Errorf("sso: unexpected issuer %q", ec.Issuer)
	}
	if !slices.Contains(ec.Audience, c.cfg.ClientID) || !slices.Contains(ec.Audience, eveAudience) {
		return Claims{}, errors.New("sso: token audience must include client id and \"EVE Online\"")
	}
	id, err := characterID(ec.Subject)
	if err != nil {
		return Claims{}, err
	}
	return Claims{
		CharacterID:   id,
		CharacterName: ec.Name,
		Scopes:        scopes(ec.Scope),
		ExpiresAt:     ec.ExpiresAt.Time,
	}, nil
}

// characterID parses a subject of the form CHARACTER:EVE:{id}.
func characterID(sub string) (int64, error) {
	rest, ok := strings.CutPrefix(sub, "CHARACTER:EVE:")
	if !ok {
		return 0, fmt.Errorf("sso: unexpected subject %q", sub)
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("sso: malformed subject %q", sub)
	}
	return id, nil
}

// scopes normalizes the scp claim, which is a string or an array of strings.
func scopes(v any) []string {
	switch s := v.(type) {
	case string:
		return []string{s}
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
