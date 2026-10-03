package web

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(assets, "static/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The sign-in screen is public: neither / nor its assets may need a session.
func TestUIAssetsArePublicWithSecurityHeaders(t *testing.T) {
	f := newFixture(t, nil, false)
	tests := []struct{ path, ctype string }{
		{"/", "text/html; charset=utf-8"},
		{"/static/app.js", "text/javascript"},
		{"/static/style.css", "text/css"},
		{"/static/chart.umd.min.js", "text/javascript"},
	}
	for _, tt := range tests {
		rec := do(f.h, http.MethodGet, tt.path) // no session cookie
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 without a session", tt.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.ctype) {
			t.Errorf("%s content type = %q, want prefix %q", tt.path, ct, tt.ctype)
		}
		assertSecurityHeaders(t, rec, tt.path)
	}
}

func TestIndexHasSignedOutAndSignedInViews(t *testing.T) {
	idx := asset(t, "index.html")
	for _, want := range []string{
		`id="signed-out"`,
		`id="signed-in"`,
		`href="/auth/login"`,
		"Sign in with EVE Online",
		`id="session-message"`,
		`id="user-name"`,
		`id="collecting"`,
		`aria-live="polite"`,
		"Collecting your wallets",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
	form := regexp.MustCompile(`(?s)<form[^>]*>`).FindAllString(idx, -1)
	found := false
	for _, tag := range form {
		if strings.Contains(tag, `method="post"`) && strings.Contains(tag, `action="/auth/logout"`) {
			found = true
		}
	}
	if !found {
		t.Errorf(`index.html lacks <form method="post" action="/auth/logout"> (forms: %v)`, form)
	}
	if !strings.Contains(idx, "Sign out") {
		t.Error("index.html lacks the Sign out button")
	}
	// Both views start hidden so a script failure never shows the wrong one.
	for _, id := range []string{"signed-out", "signed-in"} {
		re := regexp.MustCompile(`<[a-z]+[^>]*id="` + id + `"[^>]*>`)
		if tag := re.FindString(idx); !strings.Contains(tag, "hidden") {
			t.Errorf("#%s must start hidden: %s", id, tag)
		}
	}
}

// The CSP is default-src 'self': inline script/style or external references
// would be blocked, so keep them out of every asset.
func TestUIKeepsCSPValid(t *testing.T) {
	idx := asset(t, "index.html")
	for _, m := range regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script>`).FindAllStringSubmatch(idx, -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
			t.Errorf("inline script in index.html: %q", m[0])
		}
	}
	if regexp.MustCompile(`(?i)<style[\s>]`).MatchString(idx) {
		t.Error("index.html has an inline <style> block")
	}
	if regexp.MustCompile(`(?i)\sstyle\s*=`).MatchString(idx) {
		t.Error("index.html has an inline style= attribute")
	}
	if regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(idx) {
		t.Error("index.html has an inline event handler")
	}
	for _, name := range []string{"index.html", "app.js", "style.css"} {
		if regexp.MustCompile(`(?i)https?://|//cdn`).MatchString(asset(t, name)) {
			t.Errorf("%s references an external URL", name)
		}
	}
}

func TestAppJSHandlesSession(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		`/api/me`,
		`/api/wallets`,
		`/api/status`,
		"401",
		"Your session expired. Sign in again.",
		"clearTimeout",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
}
