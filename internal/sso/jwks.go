package sso

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
)

// jwksCache holds RSA public keys by kid and refetches on an unknown kid.
type jwksCache struct {
	url  string
	http *http.Client

	mu   sync.Mutex
	keys map[string]*rsa.PublicKey
}

// key returns the key for kid, fetching the JWKS when the kid is not cached.
// A single call never fetches more than once.
func (j *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	keys, err := j.fetch(ctx)
	if err != nil {
		return nil, err
	}
	j.keys = keys
	k, ok := keys[kid]
	if !ok {
		return nil, fmt.Errorf("sso: unknown signing key %q", kid)
	}
	return k, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (j *jwksCache) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := j.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sso: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sso: fetch jwks: status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("sso: decode jwks: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := k.rsaKey()
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	return keys, nil
}

func (k jwk) rsaKey() (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
}
