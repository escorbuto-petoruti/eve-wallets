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
	"time"
)

// jwksCache holds RSA public keys by kid and refetches on an unknown kid.
type jwksCache struct {
	url  string
	http *http.Client

	minRefetch time.Duration
	now        func() time.Time
	fetchFn    func(context.Context) (map[string]*rsa.PublicKey, error)

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	loaded    bool          // a fetch has succeeded
	lastFetch time.Time     // when the last successful fetch finished
	inflight  chan struct{} // closed when the running fetch ends; nil when idle
}

const defaultMinRefetch = time.Minute

func (j *jwksCache) clock() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
}

// key returns the key for kid, fetching the JWKS when the kid is not cached.
// The mutex is never held across the network call. Once keys have been loaded,
// an unknown kid triggers at most one refetch per minRefetch, so a stream of
// bogus kids cannot amplify into JWKS requests. Concurrent callers share one
// in-flight fetch.
func (j *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	for {
		j.mu.Lock()
		if k, ok := j.keys[kid]; ok {
			j.mu.Unlock()
			return k, nil
		}
		if wait := j.inflight; wait != nil {
			j.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if j.loaded && j.clock().Sub(j.lastFetch) < j.minRefetch {
			j.mu.Unlock()
			return nil, fmt.Errorf("sso: unknown signing key %q", kid)
		}
		done := make(chan struct{})
		j.inflight = done
		j.mu.Unlock()

		fetch := j.fetch
		if j.fetchFn != nil {
			fetch = j.fetchFn
		}
		keys, err := fetch(ctx)

		j.mu.Lock()
		j.inflight = nil
		if err == nil {
			j.keys, j.loaded, j.lastFetch = keys, true, j.clock()
		}
		j.mu.Unlock()
		close(done)
		if err != nil {
			return nil, err
		}
		k, ok := keys[kid]
		if !ok {
			return nil, fmt.Errorf("sso: unknown signing key %q", kid)
		}
		return k, nil
	}
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
