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

func TestIndexHasAddCharacterLinkAndCharactersList(t *testing.T) {
	idx := asset(t, "index.html")
	if !regexp.MustCompile(`<a[^>]*class="button"[^>]*href="/auth/add-character"|<a[^>]*href="/auth/add-character"[^>]*class="button"`).MatchString(idx) {
		t.Error(`index.html lacks <a class="button" href="/auth/add-character">`)
	}
	if !strings.Contains(idx, "Add character") {
		t.Error("index.html lacks the Add character copy")
	}
	bar := regexp.MustCompile(`(?s)<div id="user-bar".*?</form>`).FindString(idx)
	if !strings.Contains(bar, `id="characters"`) || !strings.Contains(bar, "/auth/add-character") {
		t.Errorf("#user-bar must hold the characters container and the add link: %s", bar)
	}
}

func TestAppRendersCharactersFromMeWithoutInnerHTML(t *testing.T) {
	js := asset(t, "app.js")
	if !strings.Contains(js, "me.characters") || !strings.Contains(js, `$("characters")`) {
		t.Error("app.js must read characters from /api/me into #characters")
	}
	if strings.Contains(js, "innerHTML") {
		t.Error("app.js must never use innerHTML")
	}
}

// Characters and corporations get their own sections, built in JS.
func TestIndexHasSectionsContainerAndSharedRange(t *testing.T) {
	idx := asset(t, "index.html")
	for _, want := range []string{`id="sections"`, `id="ranges"`, `role="group"`, `aria-label="Time range"`, `data-range="2592000"`} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
	// Per-owner widgets are created by app.js, so single-instance ids must be gone.
	for _, gone := range []string{`id="picker"`, `id="chart"`, `id="total"`, `id="latest"`, `id="chart-card"`, `id="latest-card"`} {
		if strings.Contains(idx, gone) {
			t.Errorf("index.html still has the single-instance %s", gone)
		}
	}
}

func TestAppBuildsSectionsPerKindAndOwner(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		`"Characters"`,           // one section for all characters
		`w.kind === "character"`, // split by kind
		`w.owner_id`,             // one section per corporation
		`w.owner_name`,           // titled with the owner name
		"new Chart(",             // own chart per section
		"MAX_IDS",                // 50-wallet cap kept
		"section.token",          // own request token per section
		"aria-label",             // unique canvas / range labels
		"Total",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	for _, gone := range []string{`$("chart")`, `$("picker")`, `$("total")`, "state.chart", "state.token"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still uses the single-chart %s", gone)
		}
	}
	// Empty groups are never rendered: sections come only from existing wallets.
	if !strings.Contains(js, "groupWallets") {
		t.Error("app.js must build sections from grouped wallets only")
	}
}

func TestAppDestroysSectionChartsOnSignOut(t *testing.T) {
	js := asset(t, "app.js")
	if !strings.Contains(js, ".destroy()") || !strings.Contains(js, "clearSections") {
		t.Error("app.js must destroy every section chart when sections are cleared")
	}
}

// The re-authentication notice: built in JS from status.reauth with
// textContent, linking to the add-character flow, without a second plain
// error line for the same character.
func TestAppRendersReauthNotice(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		"s.reauth",
		"/auth/add-character",
		": sign in again",
		"Sign in again",
		"EVE login screen",
		"reauth-notice",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(js, "innerHTML") {
		t.Error("app.js must never use innerHTML")
	}
	if !strings.Contains(js, `el("a", "Sign in again", "button")`) {
		t.Error("the sign-in link must be an <a class=button>")
	}
	if !strings.Contains(asset(t, "style.css"), ".reauth-notice") {
		t.Error("style.css lacks .reauth-notice")
	}
}

// Owners are shown as tabs: one visible panel, one live chart.
func TestTabsContract(t *testing.T) {
	idx := asset(t, "index.html")
	if !regexp.MustCompile(`<div id="tabs"[^>]*role="tablist"`).MatchString(idx) {
		t.Error(`index.html lacks the role="tablist" container #tabs`)
	}
	js := asset(t, "app.js")
	for _, want := range []string{
		`"role", "tab"`, `"role", "tabpanel"`,
		"aria-selected", "aria-controls", "aria-labelledby",
		"tabIndex", // roving tabindex
		`"ArrowLeft"`, `"ArrowRight"`, `"Home"`, `"End"`,
		"activateSection",
		"s.token++", // switching invalidates the previous request
		"s.chart.destroy()",
		"state.active",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	// Tab labels come from server names: textContent only.
	if !strings.Contains(js, `el("button", group.title, "tab")`) {
		t.Error("tab labels must be set through textContent")
	}
	if !strings.Contains(asset(t, "style.css"), `button.tab`) {
		t.Error("style.css lacks the tab styles")
	}
}
