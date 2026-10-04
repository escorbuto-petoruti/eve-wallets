package main

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/sso"
)

// fakeLoginSSO signs in character 1 ("Alice") without any network.
type fakeLoginSSO struct{}

func (fakeLoginSSO) AuthURL(state, challenge string) string {
	return "https://sso.test/authorize?state=" + url.QueryEscape(state) + "&code_challenge=" + url.QueryEscape(challenge)
}

func (fakeLoginSSO) Exchange(context.Context, string, string) (sso.TokenSet, error) {
	return sso.TokenSet{AccessToken: "access", RefreshToken: "refresh", ExpiresIn: time.Minute}, nil
}

func (fakeLoginSSO) Validate(context.Context, string) (sso.Claims, error) {
	return sso.Claims{CharacterID: 1, CharacterName: "Alice", Scopes: sso.WalletScopes()}, nil
}

// signIn runs the whole sign-in flow against a served app and returns a client
// holding the session cookie.
func signIn(t *testing.T, base string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("login status = %d (%v)", resp.StatusCode, err)
	}
	resp, err = c.Get(base + "/auth/callback?code=x&state=" + url.QueryEscape(loc.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}
	return c
}

func TestSSOPortWarning(t *testing.T) {
	if got := ssoPortWarning("127.0.0.1:8088"); got != "" {
		t.Errorf("fixed port warned: %q", got)
	}
	for _, addr := range []string{"127.0.0.1:9000", "[::1]:0", "localhost:8089"} {
		got := ssoPortWarning(addr)
		if !strings.Contains(got, "SSO login will not work") || !strings.Contains(got, "8088") ||
			strings.Contains(got, "\n") || strings.TrimSpace(got) != got {
			t.Errorf("ssoPortWarning(%q) = %q, want a one-line warning naming port 8088", addr, got)
		}
	}
}

func TestServeWarnsWhenThePortIsNotTheRegisteredOne(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	_, cancel, done := startServe(t, h, "--no-collect") // 127.0.0.1:0: a random port
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit = %d; stderr = %q", code, h.err.String())
	}
	warnings := 0
	for _, line := range strings.Split(h.err.String(), "\n") {
		if strings.Contains(line, "SSO login will not work") {
			warnings++
		}
	}
	if warnings != 1 {
		t.Errorf("stderr has %d port warnings, want 1: %q", warnings, h.err.String())
	}
	if strings.Contains(h.out.String(), "SSO login will not work") {
		t.Error("the warning belongs on stderr")
	}
}

func TestServeLoginTriggersACollection(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h, "--every", "1h", "--no-backfill")
	waitFor(t, "first collection", func() bool { return h.tokens.listCalls() >= 1 })
	signIn(t, base)
	waitFor(t, "collection kicked by the login", func() bool { return h.tokens.listCalls() >= 2 })
	time.Sleep(100 * time.Millisecond)
	if n := h.tokens.listCalls(); n != 2 {
		t.Errorf("collections = %d, want exactly 2 (startup + login)", n)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d; stderr = %q", code, h.err.String())
	}
}

func TestServeLoginWithNoCollectDoesNotCollect(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h, "--no-collect")
	c := signIn(t, base)
	if st := getJSON(t, c, base+"/api/me"); st["character_id"] != float64(1) {
		t.Errorf("/api/me = %v", st)
	}
	time.Sleep(50 * time.Millisecond)
	if n := h.tokens.listCalls(); n != 0 {
		t.Errorf("collector ran %d times after a login with --no-collect", n)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d", code)
	}
}

func TestServeAPIsNeedASession(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h, "--no-collect")
	resp, err := http.Get(base + "/api/wallets")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	cancel()
	<-done
}

// The collector's token cache is the one a sign-in must drop: a character that
// signs in again would otherwise keep an access token with its old scopes.
func TestServeLoginForgetsTheCachedAccessToken(t *testing.T) {
	h := newHarness(t, map[string]string{"HOME": "/home/u"})
	base, cancel, done := startServe(t, h, "--every", "1h", "--no-backfill")
	waitFor(t, "first collection", func() bool { return h.tokens.listCalls() >= 1 })
	signIn(t, base)
	if got := h.tokens.forgotten(); len(got) != 1 || got[0] != 1 {
		t.Errorf("forgotten = %v, want [1]", got)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Errorf("exit = %d; stderr = %q", code, h.err.String())
	}
}
