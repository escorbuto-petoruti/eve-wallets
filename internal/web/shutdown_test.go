package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// shutdownFixture is the standard fixture with a counting stop callback.
func shutdownFixture(t *testing.T) (*fixture, http.Handler, *atomic.Int32) {
	t.Helper()
	f := newFixture(t, nil, true)
	var calls atomic.Int32
	h := New(Deps{Store: f.st, SSO: f.sso, Now: f.clock, Shutdown: func() { calls.Add(1) }})
	return f, h, &calls
}

func shutdownReq(f *fixture, h http.Handler, mutate func(*http.Request)) *httptest.ResponseRecorder {
	return request(h, http.MethodPost, "/api/shutdown", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		r.Header.Set("Origin", "http://localhost")
		if mutate != nil {
			mutate(r)
		}
	})
}

func TestShutdownNeedsASession(t *testing.T) {
	f, h, calls := shutdownFixture(t)
	rec := request(h, http.MethodPost, "/api/shutdown", func(r *http.Request) {
		r.Header.Set("Origin", "http://localhost")
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed-out status = %d, want 401", rec.Code)
	}
	rec = shutdownReq(f, h, func(r *http.Request) {
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "bogus"})
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown session status = %d, want 401", rec.Code)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("callback called %d times, want 0", n)
	}
}

func TestShutdownRefusesCrossSiteRequests(t *testing.T) {
	cases := map[string]func(*http.Request){
		"cross origin":        func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"no origin evidence":  func(r *http.Request) { r.Header.Del("Origin") },
		"origin null":         func(r *http.Request) { r.Header.Set("Origin", "null") },
		"cross-site metadata": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site metadata":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"cross-site beats origin": func(r *http.Request) {
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		},
		"foreign host": func(r *http.Request) { r.Host = "evil.example" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f, h, calls := shutdownFixture(t)
			rec := shutdownReq(f, h, mutate)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body)
			}
			if n := calls.Load(); n != 0 {
				t.Errorf("callback called %d times, want 0", n)
			}
		})
	}
}

func TestShutdownIsPostOnly(t *testing.T) {
	f, h, calls := shutdownFixture(t)
	rec := request(h, http.MethodGet, "/api/shutdown", func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
	})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("callback called %d times, want 0", n)
	}
}

func TestShutdownAnswers202AndStopsOnce(t *testing.T) {
	f, h, calls := shutdownFixture(t)
	for i, mutate := range []func(*http.Request){
		nil, // Origin evidence
		func(r *http.Request) { r.Header.Del("Origin"); r.Header.Set("Sec-Fetch-Site", "same-origin") },
	} {
		rec := shutdownReq(f, h, mutate)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("call %d: status = %d, want 202 (body %s)", i, rec.Code, rec.Body)
		}
		assertSecurityHeaders(t, rec, "/api/shutdown")
		var got struct {
			Status string `json:"status"`
		}
		decode(t, rec, &got)
		if got.Status != "stopping" {
			t.Errorf("call %d: body = %s", i, rec.Body)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("after call %d the callback ran %d times, want exactly 1", i, n)
		}
	}
}

func TestShutdownWithoutACallbackIsNotFound(t *testing.T) {
	f := newFixture(t, nil, true)
	rec := shutdownReq(f, f.anon, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// The Quit control lives in the signed-in header, asks for confirmation inline
// and ends in a stopped state; none of it is shown to a signed-out visitor.
func TestIndexHasQuitControlInTheSignedInHeader(t *testing.T) {
	idx := asset(t, "index.html")
	bar := regexp.MustCompile(`(?s)<div id="user-bar".*?</header>`).FindString(idx)
	for _, want := range []string{`id="quit-open"`, `id="quit-confirm"`, `id="quit-yes"`, `id="quit-no"`, `id="quit-error"`} {
		if !strings.Contains(bar, want) {
			t.Errorf("#user-bar lacks %s", want)
		}
	}
	if !regexp.MustCompile(`<div id="quit-confirm"[^>]*hidden`).MatchString(idx) {
		t.Error("the confirmation must start hidden")
	}
	if !regexp.MustCompile(`<section id="stopped"[^>]*hidden`).MatchString(idx) {
		t.Error("index.html lacks a hidden #stopped section")
	}
	if !strings.Contains(idx, "eve-wallets stopped. You can close this tab.") {
		t.Error("index.html lacks the stopped message")
	}
}

func TestAppHasQuitFlow(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		`"/api/shutdown"`,
		"postJSON(",
		"quit-open", "quit-confirm", "quit-yes", "quit-no", "quit-error",
		"showStopped",
		"stopPolling()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(js, "window.confirm") || strings.Contains(js, "confirm(") {
		t.Error("the confirmation must be inline, not window.confirm")
	}
	body := regexp.MustCompile(`(?s)function showStopped\(\) \{.*?\n  \}`).FindString(js)
	if body == "" || !strings.Contains(body, "session.epoch++") || !strings.Contains(body, "stopPolling()") || !strings.Contains(body, `"stopped"`) {
		t.Errorf("showStopped must bump the epoch, stop polling and reveal #stopped: %q", body)
	}
	if !strings.Contains(asset(t, "style.css"), ".quit") {
		t.Error("style.css lacks the .quit styles")
	}
}
