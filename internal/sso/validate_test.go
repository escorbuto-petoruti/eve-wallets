package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestValidateHappyPath(t *testing.T) {
	f := newFakeSSO(t)
	c, err := f.client().Validate(context.Background(), f.token(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.CharacterID != 90000001 || c.CharacterName != "Test Pilot" {
		t.Errorf("unexpected claims: %+v", c)
	}
	if len(c.Scopes) != 2 || c.Scopes[0] != "esi-a.v1" {
		t.Errorf("scopes = %v", c.Scopes)
	}
	if time.Until(c.ExpiresAt) < 10*time.Minute {
		t.Errorf("ExpiresAt = %v", c.ExpiresAt)
	}
}

func TestValidateVariants(t *testing.T) {
	f := newFakeSSO(t)
	ctx := context.Background()

	c, err := f.client().Validate(ctx, f.token(t, func(m jwt.MapClaims) {
		m["iss"] = "login.eveonline.com"
		m["scp"] = "esi-only.v1"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scopes) != 1 || c.Scopes[0] != "esi-only.v1" {
		t.Errorf("single-string scope = %v", c.Scopes)
	}

	c, err = f.client().Validate(ctx, f.token(t, func(m jwt.MapClaims) { delete(m, "scp") }))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scopes) != 0 {
		t.Errorf("scopes = %v, want none", c.Scopes)
	}
}

func TestValidateRejections(t *testing.T) {
	f := newFakeSSO(t)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		tok  string
	}{
		{"bad signature", f.sign(t, jwt.SigningMethodRS256, other, f.kid)},
		{"wrong issuer", f.token(t, func(m jwt.MapClaims) { m["iss"] = "https://evil.example" })},
		{"expired", f.token(t, func(m jwt.MapClaims) { m["exp"] = time.Now().Add(-time.Hour).Unix() })},
		{"missing client aud", f.token(t, func(m jwt.MapClaims) { m["aud"] = []string{"EVE Online"} })},
		{"missing EVE Online aud", f.token(t, func(m jwt.MapClaims) { m["aud"] = []string{testClientID} })},
		{"aud as bare string", f.token(t, func(m jwt.MapClaims) { m["aud"] = testClientID })},
		{"alg none", f.sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, f.kid)},
		{"alg HS256", f.sign(t, jwt.SigningMethodHS256, []byte("secret"), f.kid)},
		{"malformed sub", f.token(t, func(m jwt.MapClaims) { m["sub"] = "CHARACTER:EVE:abc" })},
		{"foreign sub", f.token(t, func(m jwt.MapClaims) { m["sub"] = "USER:1" })},
		{"unknown kid", f.sign(t, jwt.SigningMethodRS256, f.key, "nope")},
		{"garbage", "not.a.jwt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.client().Validate(context.Background(), tt.tok); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestValidateJWKSCacheAndRefetch(t *testing.T) {
	f := newFakeSSO(t)
	c := f.client()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.Validate(ctx, f.token(t)); err != nil {
			t.Fatal(err)
		}
	}
	if f.jwksCalls != 1 {
		t.Errorf("jwks fetched %d times, want 1 (cached)", f.jwksCalls)
	}

	// Key rotation: a new kid triggers exactly one refetch.
	newKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	f.mu.Lock()
	f.jwksKeys["kid-2"] = &newKey.PublicKey
	f.mu.Unlock()
	if _, err := c.Validate(ctx, f.sign(t, jwt.SigningMethodRS256, newKey, "kid-2")); err != nil {
		t.Fatal(err)
	}
	if f.jwksCalls != 2 {
		t.Errorf("jwks fetched %d times, want 2", f.jwksCalls)
	}

	// Unknown kid after refetch fails and refetches only once per validation.
	if _, err := c.Validate(ctx, f.sign(t, jwt.SigningMethodRS256, f.key, "ghost")); err == nil {
		t.Fatal("expected error")
	}
	if f.jwksCalls != 3 {
		t.Errorf("jwks fetched %d times, want 3", f.jwksCalls)
	}
}
