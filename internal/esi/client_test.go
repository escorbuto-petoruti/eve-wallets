package esi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testToken = "secret-access-token-xyz"
	testUA    = "eve-wallets-test/1.0"
)

// newTestClient starts a server running h and returns a client pointed at it.
// Every request is first checked for the mandatory headers.
func newTestClient(t *testing.T, wantAuth bool, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Compatibility-Date"); got != "2020-01-01" {
			t.Errorf("X-Compatibility-Date = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != testUA {
			t.Errorf("User-Agent = %q", got)
		}
		auth := r.Header.Get("Authorization")
		if wantAuth && !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("Authorization = %q", auth)
		}
		if wantAuth && auth != "Bearer "+testToken && auth != "Bearer tok-a" && auth != "Bearer tok-b" {
			t.Errorf("Authorization = %q", auth)
		}
		if !wantAuth && auth != "" {
			t.Errorf("unexpected Authorization %q on public call", auth)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), UserAgent: testUA})
}

func TestCharacterWallet(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/characters/42/wallet" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte("123456789.12"))
	})
	got, err := c.CharacterWallet(context.Background(), testToken, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got != 12345678912 {
		t.Fatalf("cents = %d", got)
	}
}

func TestCharacterWalletExactCents(t *testing.T) {
	// A float64 round trip would corrupt this value.
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("9007199254740993.01"))
	})
	got, err := c.CharacterWallet(context.Background(), testToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 900719925474099301 {
		t.Fatalf("cents = %d", got)
	}
}

func TestCorporationWallets(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/corporations/7/wallets" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"division":1,"balance":1000.5},{"division":2,"balance":0.0}]`))
	})
	got, err := c.CorporationWallets(context.Background(), testToken, 7)
	if err != nil {
		t.Fatal(err)
	}
	want := []DivisionBalance{{Division: 1, Cents: 100050}, {Division: 2, Cents: 0}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestCharacterCorporationID(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/characters/42" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"name":"X","corporation_id":98000001,"birthday":"2010-01-01T00:00:00Z"}`))
	})
	got, err := c.CharacterCorporationID(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got != 98000001 {
		t.Fatalf("corp = %d", got)
	}
}

func journalHandler(t *testing.T, wantPath string, hits *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		hits.Add(1)
		w.Header().Set("X-Pages", "3")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`[{"id":1,"date":"2026-01-02T03:04:05Z","amount":-10.5,"balance":100.25,"ref_type":"player_trading","description":"a"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"id":2,"date":"2026-01-03T00:00:00Z","amount":5,"ref_type":"bounty_prizes","description":"no balance"}]`))
		case "3":
			_, _ = w.Write([]byte(`[{"id":3,"date":"2026-01-04T00:00:00Z","amount":0.01,"balance":0.0,"ref_type":"x","description":"c"}]`))
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}

func checkJournal(t *testing.T, got []JournalEntry) {
	t.Helper()
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ID != 1 || got[0].AmountCents != -1050 || got[0].BalanceCents == nil || *got[0].BalanceCents != 10025 {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if !got[0].Date.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("date = %v", got[0].Date)
	}
	if got[0].RefType != "player_trading" || got[0].Description != "a" {
		t.Errorf("entry 0 text = %+v", got[0])
	}
	if got[1].ID != 2 || got[1].AmountCents != 500 || got[1].BalanceCents != nil {
		t.Errorf("entry 1 = %+v", got[1])
	}
	if got[2].BalanceCents == nil || *got[2].BalanceCents != 0 || got[2].AmountCents != 1 {
		t.Errorf("entry 2 = %+v", got[2])
	}
}

func TestCharacterJournalPagination(t *testing.T) {
	var hits atomic.Int32
	c := newTestClient(t, true, journalHandler(t, "/characters/42/wallet/journal", &hits))
	got, err := c.CharacterJournal(context.Background(), testToken, 42)
	if err != nil {
		t.Fatal(err)
	}
	checkJournal(t, got)
	if hits.Load() != 3 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestCorporationJournalPagination(t *testing.T) {
	var hits atomic.Int32
	c := newTestClient(t, true, journalHandler(t, "/corporations/7/wallets/3/journal", &hits))
	got, err := c.CorporationJournal(context.Background(), testToken, 7, 3)
	if err != nil {
		t.Fatal(err)
	}
	checkJournal(t, got)
}

func TestJournalPageCap(t *testing.T) {
	var hits atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Pages", "100000")
		_, _ = w.Write([]byte(`[]`))
	})
	if _, err := c.CharacterJournal(context.Background(), testToken, 1); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != maxJournalPages {
		t.Fatalf("hits = %d, want %d", hits.Load(), maxJournalPages)
	}
}

func TestForbidden(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"Character does not have required role(s)"}`))
	})
	_, err := c.CorporationWallets(context.Background(), testToken, 7)
	if !IsForbidden(err) {
		t.Fatalf("IsForbidden false for %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 || !strings.Contains(apiErr.Message, "required role") {
		t.Fatalf("err = %#v", err)
	}
	if IsNotFound(err) {
		t.Fatal("IsNotFound true for 403")
	}
}

func TestNotFound(t *testing.T) {
	c := newTestClient(t, false, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	_, err := c.CharacterCorporationID(context.Background(), 1)
	if !IsNotFound(err) {
		t.Fatalf("IsNotFound false for %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	for _, status := range []int{420, 429} {
		c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "17")
			w.WriteHeader(status)
		})
		_, err := c.CharacterWallet(context.Background(), testToken, 1)
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if rl.RetryAfter != 17*time.Second {
			t.Fatalf("RetryAfter = %v", rl.RetryAfter)
		}
	}
}

func TestRateLimitWithoutRetryAfter(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	})
	_, err := c.CharacterWallet(context.Background(), testToken, 1)
	var rl *RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter != 0 {
		t.Fatalf("err = %#v", err)
	}
}

func TestETagReuse(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			if r.Header.Get("If-None-Match") != "" {
				t.Errorf("first request sent If-None-Match")
			}
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write([]byte("55.5"))
			return
		}
		if got := r.Header.Get("If-None-Match"); got != `"abc"` {
			t.Errorf("If-None-Match = %q", got)
		}
		w.WriteHeader(http.StatusNotModified)
	})
	for i := 0; i < 2; i++ {
		got, err := c.CharacterWallet(context.Background(), testToken, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got != 5550 {
			t.Fatalf("call %d cents = %d", i, got)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestETagKeepsPagesHeaderOn304(t *testing.T) {
	var round atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		round.Add(1)
		w.Header().Set("ETag", `"p`+r.URL.Query().Get("page")+`"`)
		w.Header().Set("X-Pages", "2")
		_, _ = w.Write([]byte(`[]`))
	})
	for i := 0; i < 2; i++ {
		if _, err := c.CharacterJournal(context.Background(), testToken, 1); err != nil {
			t.Fatal(err)
		}
	}
	if round.Load() != 2 { // pages 1 and 2 fetched once; second pass is all 304
		t.Fatalf("full fetches = %d", round.Load())
	}
}

func TestETagCacheIsPerToken(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("If-None-Match leaked across tokens")
		}
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte("1"))
	})
	for _, tok := range []string{"tok-a", "tok-b"} {
		if _, err := c.CharacterWallet(context.Background(), tok, 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestErrorsAreNotCached(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("ETag", `"bad"`)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("error response was cached")
		}
		_, _ = w.Write([]byte("1"))
	})
	if _, err := c.CharacterWallet(context.Background(), testToken, 1); err == nil {
		t.Fatal("want error")
	}
	if _, err := c.CharacterWallet(context.Background(), testToken, 1); err != nil {
		t.Fatal(err)
	}
}

func TestContextCancel(t *testing.T) {
	release := make(chan struct{})
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.CharacterWallet(ctx, testToken, 1)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	statuses := []int{400, 403, 404, 420, 429, 500}
	for _, status := range statuses {
		c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			// A misbehaving server echoing the credential must not leak it.
			_, _ = w.Write([]byte(`{"error":"bad token ` + r.Header.Get("Authorization") + `"}`))
		})
		_, err := c.CharacterWallet(context.Background(), testToken, 1)
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("status %d: token leaked: %v", status, err)
		}
	}
}

func TestMalformedBody(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not a number"))
	})
	if _, err := c.CharacterWallet(context.Background(), testToken, 1); err == nil {
		t.Fatal("want error")
	}
}

func TestOversizedBody(t *testing.T) {
	c := newTestClient(t, true, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("1", maxBodyBytes+10)))
	})
	if _, err := c.CharacterWallet(context.Background(), testToken, 1); err == nil {
		t.Fatal("want error")
	}
}
