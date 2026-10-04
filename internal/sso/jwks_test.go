package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestJWKSUnknownKidRefetchIsThrottled(t *testing.T) {
	f := newFakeSSO(t)
	c := f.client()
	now := time.Now()
	c.jwks.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := c.Validate(ctx, f.token(t)); err != nil {
		t.Fatal(err)
	}
	ghost := f.sign(t, jwt.SigningMethodRS256, f.key, "ghost")
	for i := 0; i < 10; i++ {
		if _, err := c.Validate(ctx, ghost); err == nil {
			t.Fatal("expected error")
		}
	}
	f.mu.Lock()
	calls := f.jwksCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("jwks fetched %d times, want 1 (unknown kid throttled)", calls)
	}

	now = now.Add(c.jwks.minRefetch + time.Second)
	if _, err := c.Validate(ctx, ghost); err == nil {
		t.Fatal("expected error")
	}
	f.mu.Lock()
	calls = f.jwksCalls
	f.mu.Unlock()
	if calls != 2 {
		t.Fatalf("jwks fetched %d times after interval, want 2", calls)
	}
}

func TestJWKSKeyDoesNotHoldLockDuringFetch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	j := &jwksCache{minRefetch: time.Minute, now: time.Now}
	var fetches atomic.Int32
	j.fetchFn = func(ctx context.Context) (map[string]*rsa.PublicKey, error) {
		fetches.Add(1)
		once.Do(func() { close(started) })
		<-release
		k, _ := rsa.GenerateKey(rand.Reader, 1024)
		return map[string]*rsa.PublicKey{"a": &k.PublicKey}, nil
	}
	done := make(chan struct{})
	go func() { _, _ = j.key(context.Background(), "a"); close(done) }()
	<-started

	// A cancelled caller must return promptly while a fetch is in flight.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := make(chan error, 1)
	go func() { _, err := j.key(ctx, "b"); res <- err }()
	select {
	case err := <-res:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("key blocked behind an in-flight fetch despite a cancelled context")
	}
	close(release)
	<-done
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
}
