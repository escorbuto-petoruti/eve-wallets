package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
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

func newFailingJWKS(fetches *atomic.Int32, now *time.Time, fail *atomic.Bool) *jwksCache {
	k, _ := rsa.GenerateKey(rand.Reader, 1024)
	return &jwksCache{
		minRefetch:           time.Minute,
		minRetryAfterFailure: 10 * time.Second,
		now:                  func() time.Time { return *now },
		fetchFn: func(ctx context.Context) (map[string]*rsa.PublicKey, error) {
			fetches.Add(1)
			if fail.Load() {
				return nil, errors.New("boom")
			}
			return map[string]*rsa.PublicKey{"a": &k.PublicKey}, nil
		},
	}
}

func TestJWKSFailedFetchIsThrottledThenRetried(t *testing.T) {
	var fetches atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	now := time.Now()
	j := newFailingJWKS(&fetches, &now, &fail)
	ctx := context.Background()

	if _, err := j.key(ctx, "a"); err == nil {
		t.Fatal("expected error")
	}
	for i := 0; i < 10; i++ {
		_, err := j.key(ctx, "a")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v, want it to mention the fetch failure", err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d within backoff, want 1", n)
	}

	now = now.Add(11 * time.Second)
	if _, err := j.key(ctx, "a"); err == nil {
		t.Fatal("expected error")
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d after backoff, want 2", n)
	}
}

func TestJWKSConcurrentWaitersOnFailingFetchShareOneFetch(t *testing.T) {
	var fetches atomic.Int32
	now := time.Now()
	started := make(chan struct{})
	release := make(chan struct{})
	j := &jwksCache{
		minRefetch:           time.Minute,
		minRetryAfterFailure: 10 * time.Second,
		now:                  func() time.Time { return now },
		fetchFn: func(ctx context.Context) (map[string]*rsa.PublicKey, error) {
			fetches.Add(1)
			close(started)
			<-release
			return nil, errors.New("boom")
		},
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := j.key(context.Background(), "a"); errs <- err }()
	<-started
	for i := 0; i < 7; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := j.key(context.Background(), "a"); errs <- err }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("expected error")
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetches = %d, want 1 (no thundering herd)", n)
	}
}

func TestJWKSSuccessClearsFailureAndKeepsSuccessThrottle(t *testing.T) {
	var fetches atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	now := time.Now()
	j := newFailingJWKS(&fetches, &now, &fail)
	ctx := context.Background()

	if _, err := j.key(ctx, "a"); err == nil {
		t.Fatal("expected error")
	}
	fail.Store(false)
	now = now.Add(11 * time.Second)
	if _, err := j.key(ctx, "a"); err != nil {
		t.Fatalf("key after recovery: %v", err)
	}
	// Unknown kid right after a success: success throttle applies, no fetch.
	_, err := j.key(ctx, "ghost")
	if err == nil || !strings.HasPrefix(err.Error(), "sso: unknown signing key") || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("err = %v, want unknown-key throttle message mentioning retry", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
	now = now.Add(time.Minute + time.Second)
	if _, err := j.key(ctx, "ghost"); err == nil {
		t.Fatal("expected error")
	}
	if n := fetches.Load(); n != 3 {
		t.Fatalf("fetches = %d after throttle, want 3", n)
	}
}
