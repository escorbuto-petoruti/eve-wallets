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
		// The assets are embedded in the binary: without revalidation a browser
		// keeps showing the previous build after a deploy.
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q, want no-cache", tt.path, cc)
		}
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
		// The only allowed external reference is the EVE image server (img-src).
		src := strings.ReplaceAll(asset(t, name), "https://images.evetech.net/", "")
		if regexp.MustCompile(`(?i)https?://|//cdn`).MatchString(src) {
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
		"new Chart(",             // own chart per panel
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
		"destroyCharts(s)",
		"state.active",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	// Tab labels come from server names: textContent only.
	if !strings.Contains(js, `el("button", label, "tab")`) || !strings.Contains(js, `makeTab("tab-" + idx, group.title`) {
		t.Error("tab labels must be set through textContent")
	}
	if !strings.Contains(asset(t, "style.css"), `button.tab`) {
		t.Error("style.css lacks the tab styles")
	}
}

// Wallets are listed inside their owner's tab, so the owner name must not be
// prefixed to every wallet label.
func TestWalletLabelDoesNotRepeatTheOwner(t *testing.T) {
	js := asset(t, "app.js")
	if strings.Contains(js, "w.owner_name + \" \\u00b7 \"") {
		t.Error("app.js still prefixes the owner name to the wallet label")
	}
}

// Portraits and logos come from the EVE image server, which the CSP allows
// for img-src only. Ids are validated before they reach a URL.
func TestAppBuildsEveImageURLsFromSafeIds(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		"https://images.evetech.net/characters/",
		"/portrait?size=64",
		"https://images.evetech.net/corporations/",
		"/logo?size=64",
		"Number.isSafeInteger",
		"> 0",
		`"alt", ""`,
		`"width"`, `"height"`,
		`"lazy"`, `"async"`,
		`addEventListener("error"`,
		".remove()",
		"eve-img",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(js, "innerHTML") || strings.Contains(js, ".style.") || strings.Contains(js, "onerror") {
		t.Error("app.js must not use innerHTML, inline styles or onerror")
	}
}

func TestStyleHasEveImageClass(t *testing.T) {
	css := asset(t, "style.css")
	for _, want := range []string{".eve-img", "vertical-align"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}

// Each wallet gets its own small-multiple panel with its own chart and Y
// scale; there are no selection checkboxes any more.
func TestAppBuildsSmallMultiplePanels(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		"buildPanel", "drawPanels", "drawSpark",
		`stepped: "before"`,
		"legend: { display: false }",
		`"big num"`, `"delta "`, // current balance and delta
		`"Up"`, `"Down"`, `"Flat"`, // text label, not color alone
		"Showing the first ", // omitted wallets are announced
		`q.set("total", "1")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	for _, gone := range []string{"totalBox", "section.selected", `cb.type = "checkbox"`, "Select at least one wallet", "fieldset"} {
		if strings.Contains(js, gone) {
			t.Errorf("app.js still has the removed selection UI %q", gone)
		}
	}
	css := asset(t, "style.css")
	for _, want := range []string{".panels", "auto-fill", "minmax(280px", ".panel", ".spark", "height: 120px"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}

// Corporation wallet panels offer an inline rename. The UI only hides the
// control for wallets the server would refuse; the server stays the guard.
func TestAppHasInlineRename(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		"canRename",
		`w.kind === "corporation"`,
		"w.division !== 1",
		`w.name_source !== "esi"`,
		"buildRenameControl",
		`"Rename"`, `"Save"`, `"Cancel"`, `"Reset name"`,
		`"/label"`,
		`method: "POST"`,
		`credentials: "same-origin"`,
		`"Content-Type": "application/json"`,
		"JSON.stringify",
		"MAX_NAME",
		"input.maxLength",
		`"aria-live", "polite"`, // errors and results are announced
		`"role", "status"`,
		`"for", inputId`,  // the field has a label
		"input.focus()",   // focus moves into the form on open
		"openBtn.focus()", // and back on close
		`"Escape"`,
		"rowNames", // the balances table row is updated too
		"titleText.nodeValue",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	// A server-provided name is shown via the DOM, never parsed as HTML.
	if strings.Contains(js, "innerHTML") || strings.Contains(js, ".style.") {
		t.Error("app.js must not use innerHTML or inline styles")
	}
	css := asset(t, "style.css")
	for _, want := range []string{".rename-form", ".rename-actions", ".rename-status", ".rename-label"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}

// Every wallet panel (not the Total) opens a movements view backed by the
// journal endpoint: filters, Previous/Next keyset paging, an announced status, focus
// management and a close button. Server text only goes through textContent.
func TestAppHasMovementsView(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		"buildMovements",
		"openMovements",
		`"Movements"`,
		`"Close"`,
		`"Previous"`,
		`"Next"`,
		`"Page "`,
		"cur.stack",
		"movements-pager",
		`/journal?`,
		"encodeURIComponent(String(w.id))",
		"ref_type",
		"next_cursor",
		"ref_types",
		`"cursor=" +`,
		`type = "date"`,
		`"Type"`, `"From"`, `"To"`, `"All types"`,
		`"Date"`, `"Amount (ISK)"`, `"Description"`,
		`"No movements found."`,
		`"+" + formatISK`, // the sign is text, not only a color
		"formatTime(",
		"formatISK(",
		"getJSON(",
		`"aria-live", "polite"`,
		`"role", "status"`,
		`"for", `,
		"closeBtn.focus()",
		"trigger.focus()", // focus returns to the Movements button on close
		`"scroll movements-scroll movements-body"`,
		`createElement("dialog")`,
		"showModal",
		`"cancel"`,
		`"close"`,
		`"aria-labelledby"`,
		"movements-dialog",
		"movements-body",
		`"tabindex", "0"`,
		`"aria-label", "Movements table"`,
		`setAttribute("title"`, // the full text stays reachable under the ellipsis
		"scrollTop = 0",
		"movements-desc",
		`"Escape"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(js, "innerHTML") || strings.Contains(js, ".style.") {
		t.Error("app.js must not use innerHTML or inline styles")
	}
	if strings.Contains(js, "Load more") || strings.Contains(js, "movements-more") {
		t.Error("app.js must page with Previous/Next, not Load more")
	}
	css := asset(t, "style.css")
	for _, want := range []string{".movements-pager", ".movements", ".movements-filters", ".movements-status", ".amount", ".movements-scroll", ".movements-desc", ".movements-dialog", ".movements-body", "dialog::backdrop", "100dvh", "table-layout: fixed", "position: sticky", "text-overflow: ellipsis"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
	for _, bad := range []string{"min-width: 36rem", "min(60vh, 32rem)"} {
		if strings.Contains(css, bad) {
			t.Errorf("style.css must not contain %q", bad)
		}
	}
	idx := asset(t, "index.html")
	if strings.Contains(idx, "<script>") || strings.Contains(idx, "style=") {
		t.Error("index.html must not carry inline scripts or styles")
	}
}

// The loyalty points view is the last tab of the owner tab bar, so the bar is
// one tablist and there is no standalone card.
func TestIndexHasLoyaltyPanelInsteadOfCard(t *testing.T) {
	idx := asset(t, "index.html")
	for _, want := range []string{
		`id="loyalty-panel"`,
		`id="loyalty-title"`,
		"Loyalty points",
		`id="loyalty-status"`,
		`id="loyalty"`,
		`aria-labelledby="tab-loyalty"`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html lacks %q", want)
		}
	}
	if strings.Contains(idx, `id="loyalty-card"`) {
		t.Error("index.html must not keep the standalone #loyalty-card section")
	}
	if tag := regexp.MustCompile(`<[a-z]+[^>]*id="loyalty-panel"[^>]*>`).FindString(idx); !strings.Contains(tag, `role="tabpanel"`) || !strings.Contains(tag, "hidden") {
		t.Errorf("#loyalty-panel must be a hidden tabpanel: %s", tag)
	}
	if tag := regexp.MustCompile(`<[a-z]+[^>]*id="loyalty-status"[^>]*>`).FindString(idx); !strings.Contains(tag, `aria-live="polite"`) || !strings.Contains(tag, `role="status"`) {
		t.Errorf("#loyalty-status must be a polite live status: %s", tag)
	}
	// The panel lives outside #sections, which is emptied on every render.
	if i, j := strings.Index(idx, `id="loyalty-panel"`), strings.Index(idx, `id="sections"`); i < j {
		t.Error("#loyalty-panel must come after #sections")
	}
	for _, want := range []string{`id="tabs"`, `role="tablist"`, `id="sections"`, `id="status-card"`} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html lost %q", want)
		}
	}
}

// The loyalty tab is built with the shared tab pattern, loads lazily and hides
// the time-range card.
func TestAppHasLoyaltyTab(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		`makeTab("tab-loyalty", "Loyalty points", "loyalty-panel"`,
		"activateLoyalty",
		"state.lpActive",
		"state.lpLoadedAt",
		"LP_STALE_MS",
		`$("controls").hidden = state.lpActive || !state.hasData`,
		`insertBefore(tab, state.lpTab)`,
		"state.lpTab",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if n := strings.Count(js, `"role", "tab"`); n != 1 {
		t.Errorf("tabs must come from one shared helper, found %d role=tab sites", n)
	}
	if strings.Contains(js, `loyalty-card`) {
		t.Error("app.js still references #loyalty-card")
	}
	// Lazy: init must not load the loyalty points itself.
	init := regexp.MustCompile(`(?s)function init\(\).*?\n  }\n`).FindString(js)
	if strings.Contains(init, "loadLoyalty()") {
		t.Error("init must not load loyalty points eagerly")
	}
	// The bar is keyboard-navigable as one tablist including the loyalty tab.
	keys := regexp.MustCompile(`(?s)function onTabKey.*?\n  }\n`).FindString(js)
	if !strings.Contains(keys, "allTabs()") {
		t.Error("onTabKey must navigate over owner tabs and the loyalty tab")
	}
}

func TestAppRendersLoyaltyPoints(t *testing.T) {
	js := asset(t, "app.js")
	for _, want := range []string{
		`/api/loyalty`,
		`needs_reauth`,
		`"/auth/add-character"`,
		`reauth-notice`,
		`eveImage("corporation"`,
		`eveImage("character"`,
		`Intl.NumberFormat`,
		`$("loyalty-status")`,
		`No loyalty points`,
		`Cannot load loyalty points`,
		`clearLoyalty`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	if strings.Contains(js, "innerHTML") {
		t.Error("app.js must never use innerHTML")
	}
	if regexp.MustCompile(`\.style\.`).MatchString(js) {
		t.Error("app.js must not set inline styles (CSP): use classes")
	}
	// Signing out clears the view so another user never sees stale data.
	show := regexp.MustCompile(`(?s)function showSignedOut.*?\n  }\n`).FindString(js)
	if !strings.Contains(show, "clearLoyalty()") {
		t.Error("showSignedOut must clear the loyalty view")
	}
}

func TestStyleHasLoyaltyClasses(t *testing.T) {
	css := asset(t, "style.css")
	for _, want := range []string{".loyalty-character", ".loyalty-corp", ".loyalty-table"} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}
