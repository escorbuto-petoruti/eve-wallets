package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

var base = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

// fixture serves the app over a real SQLite file. f.h is the handler seen by a
// browser signed in as Alice (user 1) when the request carries no cookie of
// its own; f.anon is the bare handler.
type fixture struct {
	h, anon        http.Handler
	st             *store.Store
	dbPath         string
	charID, corpID int64 // wallet ids
	sso            *fakeSSO
	now            atomic.Int64 // unix seconds
	logins         []int64
	forgot         []forgotten
	aliceCookie    string
	mu             sync.Mutex
}

func (f *fixture) clock() time.Time { return time.Unix(f.now.Load(), 0).UTC() }

// forgotten records an OnTokenSaved call and the refresh token stored at that
// moment: the hook must run after the save.
type forgotten struct {
	id      int64
	refresh string
}

func (f *fixture) forgotten() []forgotten {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forgotten(nil), f.forgot...)
}

func (f *fixture) onTokenSaved(id int64) {
	tok, _, _ := f.st.GetToken(context.Background(), id)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, forgotten{id, tok.RefreshToken})
}

// wantForgotten fails unless exactly one cached token was dropped, for id,
// after the new refresh token was stored.
func (f *fixture) wantForgotten(t *testing.T, id int64, refresh string) {
	t.Helper()
	if got := f.forgotten(); len(got) != 1 || got[0].id != id || got[0].refresh != refresh {
		t.Errorf("OnTokenSaved calls = %+v, want one for %d after storing %q", got, id, refresh)
	}
}

func (f *fixture) loggedIn() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.logins...)
}

// newSession stores a session of userID and returns the cookie value.
func (f *fixture) newSession(t *testing.T, userID int64, ttl time.Duration) string {
	t.Helper()
	val := "session-value-" + strconv.FormatInt(userID, 10) + "-" + strconv.FormatInt(int64(ttl), 10)
	if err := f.st.CreateSession(context.Background(), hashSession(val), userID, f.clock(), f.clock().Add(ttl)); err != nil {
		t.Fatal(err)
	}
	return val
}

func (f *fixture) link(t *testing.T, userID, walletID int64) {
	t.Helper()
	if err := f.st.LinkWallet(context.Background(), userID, walletID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addUser(t *testing.T, id int64, name string) {
	t.Helper()
	if err := f.st.UpsertUser(context.Background(), id, name, f.clock()); err != nil {
		t.Fatal(err)
	}
}

func newFixture(t *testing.T, status func() StatusSnapshot, seed bool) *fixture {
	t.Helper()
	path := t.TempDir() + "/w.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, dbPath: path, sso: newFakeSSO()}
	f.now.Store(base.Unix())
	f.anon = New(Deps{Store: st, Status: status, SSO: f.sso, Now: f.clock, OnTokenSaved: f.onTokenSaved, OnLogin: func(id int64) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.logins = append(f.logins, id)
	}})
	f.addUser(t, 1, "Alice")
	f.aliceCookie = f.newSession(t, 1, 7*24*time.Hour)
	f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		}
		f.anon.ServeHTTP(w, r)
	})
	if !seed {
		return f
	}
	ctx := context.Background()
	f.charID, err = st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 1, OwnerName: "Alice <b>", Division: 0})
	if err != nil {
		t.Fatal(err)
	}
	f.corpID, err = st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 9, OwnerName: "Corp", Division: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{f.charID, f.corpID} {
		if err := st.LinkWallet(ctx, 1, id); err != nil {
			t.Fatal(err)
		}
	}
	mustSnap := func(id int64, at time.Time, cents int64) {
		t.Helper()
		if err := st.AddSnapshot(ctx, id, at, cents); err != nil {
			t.Fatal(err)
		}
	}
	mustSnap(f.charID, base, 1000)
	mustSnap(f.charID, base.Add(time.Hour), 2000)
	mustSnap(f.corpID, base.Add(time.Hour), 500)
	return f
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, nil)
	req.Host = "localhost"
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func assertSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder, target string) {
	t.Helper()
	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; img-src 'self' https://images.evetech.net",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s %s: header %s = %q, want %q", target, strconv.Itoa(rec.Code), k, got, v)
		}
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("%s: unexpected CORS header %q", target, got)
	}
}

func TestWallets(t *testing.T) {
	f := newFixture(t, nil, true)
	rec := do(f.h, http.MethodGet, "/api/wallets")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q", ct)
	}
	var got struct {
		Wallets []struct {
			ID          int64  `json:"id"`
			Kind        string `json:"kind"`
			OwnerID     int64  `json:"owner_id"`
			OwnerName   string `json:"owner_name"`
			Division    int    `json:"division"`
			Cents       *int64 `json:"cents"`
			BalanceTime *int64 `json:"balance_time"`
		} `json:"wallets"`
	}
	decode(t, rec, &got)
	if len(got.Wallets) != 2 {
		t.Fatalf("wallets = %d, want 2", len(got.Wallets))
	}
	w := got.Wallets[0]
	if w.Kind != "character" || w.OwnerID != 1 || w.OwnerName != "Alice <b>" || w.Division != 0 ||
		w.Cents == nil || *w.Cents != 2000 || w.BalanceTime == nil || *w.BalanceTime != base.Add(time.Hour).Unix() {
		t.Fatalf("unexpected first wallet: %+v", w)
	}
	if got.Wallets[1].Kind != "corporation" || got.Wallets[1].Division != 3 {
		t.Fatalf("unexpected second wallet: %+v", got.Wallets[1])
	}
}

func TestWalletsExposeNames(t *testing.T) {
	f := newFixture(t, nil, true)
	ctx := context.Background()
	esiID, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 9, OwnerName: "Corp", Division: 4})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 1, esiID)
	if err := f.st.SetESIName(ctx, esiID, "Ops"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetESIName(ctx, f.corpID, "From ESI"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetLabel(ctx, f.corpID, "Mining <i>fund</i>"); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Wallets []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			NameSource string `json:"name_source"`
		} `json:"wallets"`
	}
	decode(t, do(f.h, http.MethodGet, "/api/wallets"), &got)
	want := map[int64][2]string{
		f.charID: {"Alice <b>", "default"},
		f.corpID: {"Mining <i>fund</i>", "custom"},
		esiID:    {"Ops", "esi"},
	}
	if len(got.Wallets) != len(want) {
		t.Fatalf("wallets = %d, want %d", len(got.Wallets), len(want))
	}
	for _, w := range got.Wallets {
		if exp := want[w.ID]; w.Name != exp[0] || w.NameSource != exp[1] {
			t.Errorf("wallet %d: name %q source %q, want %q %q", w.ID, w.Name, w.NameSource, exp[0], exp[1])
		}
	}
}

func TestWalletsDefaultDivisionName(t *testing.T) {
	f := newFixture(t, nil, true)
	var got struct {
		Wallets []struct {
			Name       string `json:"name"`
			NameSource string `json:"name_source"`
		} `json:"wallets"`
	}
	decode(t, do(f.h, http.MethodGet, "/api/wallets"), &got)
	if w := got.Wallets[1]; w.Name != "Division 3" || w.NameSource != "default" {
		t.Fatalf("corporation wallet = %+v", w)
	}
}

func TestWalletsWithoutBalanceHaveNullCents(t *testing.T) {
	f := newFixture(t, nil, false)
	id, err := f.st.UpsertWallet(context.Background(), store.Wallet{Kind: store.KindCharacter, OwnerID: 5, OwnerName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 1, id)
	rec := do(f.h, http.MethodGet, "/api/wallets")
	if !strings.Contains(rec.Body.String(), `"cents":null`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestEmptyDatabase(t *testing.T) {
	f := newFixture(t, nil, false)
	for target, want := range map[string]string{
		"/api/wallets": `{"wallets":[]}`,
		"/api/series":  `{"series":[]}`,
	} {
		rec := do(f.h, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Errorf("%s body = %s, want %s", target, got, want)
		}
	}
}

type seriesResp struct {
	Series []struct {
		WalletID int64 `json:"wallet_id"`
		Points   []struct {
			T     int64 `json:"t"`
			Cents int64 `json:"cents"`
		} `json:"points"`
	} `json:"series"`
	Total []struct {
		T     int64 `json:"t"`
		Cents int64 `json:"cents"`
	} `json:"total"`
}

func TestSeries(t *testing.T) {
	f := newFixture(t, nil, true)
	ids := strconv.FormatInt(f.charID, 10) + "," + strconv.FormatInt(f.corpID, 10)
	q := url.Values{"wallet_ids": {ids}, "from": {base.Add(-time.Hour).Format(time.RFC3339)}, "to": {base.Add(2 * time.Hour).Format(time.RFC3339)}}
	rec := do(f.h, http.MethodGet, "/api/series?"+q.Encode())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var got seriesResp
	decode(t, rec, &got)
	if len(got.Series) != 2 || len(got.Series[0].Points) != 2 || len(got.Series[1].Points) != 1 {
		t.Fatalf("unexpected series: %s", rec.Body)
	}
	p := got.Series[0].Points[1]
	if p.T != base.Add(time.Hour).Unix() || p.Cents != 2000 {
		t.Fatalf("point = %+v", p)
	}
	if got.Total != nil {
		t.Fatalf("total present without total=1")
	}
}

func TestSeriesRangeFilters(t *testing.T) {
	f := newFixture(t, nil, true)
	q := url.Values{"wallet_ids": {strconv.FormatInt(f.charID, 10)}, "from": {base.Add(30 * time.Minute).Format(time.RFC3339)}}
	var got seriesResp
	decode(t, do(f.h, http.MethodGet, "/api/series?"+q.Encode()), &got)
	if len(got.Series) != 1 || len(got.Series[0].Points) != 1 {
		t.Fatalf("series = %+v", got.Series)
	}
}

func TestSeriesTotal(t *testing.T) {
	f := newFixture(t, nil, true)
	ids := strconv.FormatInt(f.charID, 10) + "," + strconv.FormatInt(f.corpID, 10)
	var got seriesResp
	decode(t, do(f.h, http.MethodGet, "/api/series?total=1&wallet_ids="+ids), &got)
	if len(got.Total) != 2 || got.Total[0].Cents != 1000 || got.Total[1].Cents != 2500 {
		t.Fatalf("total = %+v", got.Total)
	}
}

func TestSeriesUnknownWalletIsEmpty(t *testing.T) {
	f := newFixture(t, nil, true)
	rec := do(f.h, http.MethodGet, "/api/series?wallet_ids=4242")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"series":[{"wallet_id":4242,"points":[]}]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestSeriesDuplicateIDsCollapse(t *testing.T) {
	f := newFixture(t, nil, true)
	id := strconv.FormatInt(f.charID, 10)
	var got seriesResp
	decode(t, do(f.h, http.MethodGet, "/api/series?wallet_ids="+id+","+id), &got)
	if len(got.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(got.Series))
	}
}

func TestSeriesValidation(t *testing.T) {
	f := newFixture(t, nil, true)
	many := make([]string, maxWalletIDs+1)
	for i := range many {
		many[i] = strconv.Itoa(i + 1)
	}
	tests := []struct{ name, query string }{
		{"non numeric id", "wallet_ids=abc"},
		{"empty id", "wallet_ids=1,,2"},
		{"negative id", "wallet_ids=-1"},
		{"zero id", "wallet_ids=0"},
		{"overflow id", "wallet_ids=99999999999999999999"},
		{"too many ids", "wallet_ids=" + strings.Join(many, ",")},
		{"bad from", "wallet_ids=1&from=yesterday"},
		{"bad to", "wallet_ids=1&to=2026-13-45T00:00:00Z"},
		{"to before from", "wallet_ids=1&from=2026-05-02T00:00:00Z&to=2026-05-01T00:00:00Z"},
		{"range too large", "wallet_ids=1&from=1990-01-01T00:00:00Z&to=2026-05-01T00:00:00Z"},
		{"bad total", "wallet_ids=1&total=maybe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(f.h, http.MethodGet, "/api/series?"+tt.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			var e struct {
				Error string `json:"error"`
			}
			decode(t, rec, &e)
			if e.Error == "" || len(e.Error) > 120 {
				t.Fatalf("error message = %q", e.Error)
			}
			assertSecurityHeaders(t, rec, tt.query)
		})
	}
}

func TestStatus(t *testing.T) {
	rep := collector.Report{
		TakenAt:       base,
		Snapshots:     make([]collector.Snapshot, 3),
		JournalPoints: 42,
		// Attributed to the signed-in user's character: a skip without an
		// owner identity is never shown to anyone.
		Skipped:     []collector.Skip{{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Reason: "missing corporation role", UserID: 1}},
		Errors:      []collector.ItemError{{Owner: "Bob", Err: errBoom{}}}, // no identity: run-level, shown
		RateLimited: true,
		RetryAfter:  90 * time.Second,
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	f.saveChar(t, 1, 1, "Alice")
	rec := do(f.h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		TakenAt           *int64 `json:"taken_at"`
		Snapshots         int    `json:"snapshots"`
		JournalPoints     int    `json:"journal_points"`
		Skipped           []struct{ Owner, Reason string }
		Errors            []string `json:"errors"`
		RateLimited       bool     `json:"rate_limited"`
		RetryAfterSeconds int      `json:"retry_after_seconds"`
	}
	decode(t, rec, &got)
	if got.TakenAt == nil || *got.TakenAt != base.Unix() || got.Snapshots != 3 || got.JournalPoints != 42 || !got.RateLimited ||
		got.RetryAfterSeconds != 90 || len(got.Skipped) != 1 || got.Skipped[0].Reason != "missing corporation role" ||
		len(got.Errors) != 1 || got.Errors[0] != "Bob: boom" {
		t.Fatalf("unexpected status: %s", rec.Body)
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func TestStatusWithoutProvider(t *testing.T) {
	f := newFixture(t, nil, false)
	rec := do(f.h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"taken_at":null`, `"skipped":[]`, `"errors":[]`, `"rate_limited":false`, `"journal_points":0`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s missing %s", body, want)
		}
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	f := newFixture(t, nil, true)
	targets := []string{"/", "/static/app.js", "/static/style.css", "/api/wallets", "/api/series", "/api/status",
		"/api/series?wallet_ids=x", "/nope", "/static/", "/static/missing.js"}
	for _, target := range targets {
		assertSecurityHeaders(t, do(f.h, http.MethodGet, target), target)
	}
	assertSecurityHeaders(t, do(f.h, http.MethodPost, "/api/wallets"), "POST")
}

func TestMethods(t *testing.T) {
	f := newFixture(t, nil, true)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions} {
		rec := do(f.h, m, "/api/wallets")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s Allow = %q", m, got)
		}
	}
	rec := do(f.h, http.MethodHead, "/api/wallets")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD status = %d, body len = %d", rec.Code, rec.Body.Len())
	}
}

func TestIndexAndStatic(t *testing.T) {
	f := newFixture(t, nil, false)
	tests := []struct{ path, ctype string }{
		{"/", "text/html"},
		{"/static/app.js", "text/javascript"},
		{"/static/style.css", "text/css"},
		{"/static/chart.umd.min.js", "text/javascript"},
	}
	for _, tt := range tests {
		rec := do(f.h, http.MethodGet, tt.path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", tt.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.ctype) {
			t.Errorf("%s content type = %q, want %s", tt.path, ct, tt.ctype)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s empty body", tt.path)
		}
	}
	if body := do(f.h, http.MethodGet, "/").Body.String(); !strings.Contains(body, "Collecting your wallets") {
		t.Error("index lacks the empty-state hint")
	}
}

func TestNoDirectoryListingOrUnknownPaths(t *testing.T) {
	f := newFixture(t, nil, false)
	for _, p := range []string{"/static/", "/static", "/static/missing.js", "/nope", "/static/../go.mod", "/index.html"} {
		rec := do(f.h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently &&
			rec.Code != http.StatusTemporaryRedirect {
			t.Errorf("%s status = %d, want 404", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "chart.umd.min.js") && !strings.Contains(p, "index") {
			t.Errorf("%s leaks a listing", p)
		}
	}
}

func TestAppFilesHaveNoExternalURLs(t *testing.T) {
	for _, name := range []string{"static/index.html", "static/app.js", "static/style.css"} {
		b, err := fs.ReadFile(assets, name)
		if err != nil {
			t.Fatal(err)
		}
		// The only allowed external reference is the EVE image server (img-src).
		s := strings.ReplaceAll(string(b), "https://images.evetech.net/", "")
		if strings.Contains(s, "http://") || strings.Contains(s, "https://") || strings.Contains(s, "//cdn") {
			t.Errorf("%s references an external URL", name)
		}
	}
	idx, _ := fs.ReadFile(assets, "static/index.html")
	if strings.Contains(strings.ToLower(string(idx)), "<script>") || strings.Contains(string(idx), "onclick=") ||
		strings.Contains(string(idx), "style=") {
		t.Error("index.html must not contain inline scripts or styles")
	}
}

func TestAppJSNeverUsesInnerHTML(t *testing.T) {
	b, err := fs.ReadFile(assets, "static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(string(b), bad) {
			t.Errorf("app.js uses %s", bad)
		}
	}
}

// The status is scoped to the signed-in user: a skip or attributed error is
// shown only when the user can see its owner; a skip without an identity is
// never shown (it belongs to someone, a missing identity is a bug); a skip is
// also shown only to the user whose character produced it, even on a
// corporation another user can see; an error
// without an identity is a run-level error and is shown to every user. The
// counters stay global: they describe the process cycle.
func TestStatusScopesSkipsAndErrorsToUser(t *testing.T) {
	rep := collector.Report{
		TakenAt:       base,
		Snapshots:     make([]collector.Snapshot, 2),
		JournalPoints: 7,
		Skipped: []collector.Skip{
			{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Reason: "missing scope esi-wallet.read_corporation_wallets.v1", UserID: 1},
			{OwnerKind: store.KindCorporation, OwnerID: 20, Owner: "Cuervos Imperiales", Reason: "missing corporation role", UserID: 2},
			// Another user's character hit the 403 on a corporation Bob can see: not Bob's skip.
			{OwnerKind: store.KindCorporation, OwnerID: 20, Owner: "Cuervos Imperiales", Reason: "missing corporation role (other user)", UserID: 1},
			{Owner: "Mystery", Reason: "missing corporation role"}, // no identity: a bug, never shown
		},
		Errors: []collector.ItemError{
			{OwnerKind: store.KindCorporation, OwnerID: 20, Owner: "Cuervos Imperiales", Err: errBoom{}},
		},
	}
	snap := StatusFromReport(rep)
	snap.Errors = append(snap.Errors, ErrorItem{Message: "collection failed: boom"}) // run-level, no owner
	f := newFixture(t, func() StatusSnapshot { return snap }, false)
	f.addUser(t, 2, "Bob")
	f.saveChar(t, 1, 1, "Alice")
	ctx := context.Background()
	charID, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 1, OwnerName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	corpID, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 20, OwnerName: "Cuervos Imperiales", Division: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 1, charID)
	f.link(t, 2, corpID)

	type item struct {
		Owner  string
		Reason string
	}
	var view func(rec *httptest.ResponseRecorder) (skipped []item, errs []string)
	view = func(rec *httptest.ResponseRecorder) (skipped []item, errs []string) {
		var got struct {
			Skipped []item   `json:"skipped"`
			Errors  []string `json:"errors"`
		}
		decode(t, rec, &got)
		return got.Skipped, got.Errors
	}

	// Alice sees her own skip and the run-level error, and nothing of Bob's.
	rec := do(f.h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var counters struct {
		TakenAt       *int64 `json:"taken_at"`
		Snapshots     int    `json:"snapshots"`
		JournalPoints int    `json:"journal_points"`
	}
	decode(t, rec, &counters)
	if counters.TakenAt == nil || *counters.TakenAt != base.Unix() || counters.Snapshots != 2 || counters.JournalPoints != 7 {
		t.Errorf("counters = %+v, want taken_at %d, 2 snapshots, 7 journal points", counters, base.Unix())
	}
	skipped, errs := view(rec)
	if len(skipped) != 1 || skipped[0].Owner != "Alice" {
		t.Errorf("skipped = %+v, want only Alice's own skip", skipped)
	}
	if len(errs) != 1 || errs[0] != "collection failed: boom" {
		t.Errorf("errors = %q, want only the run-level error", errs)
	}

	// Bob sees his corporation's skip and error, and nothing of Alice's.
	bob := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	bob.Host = "localhost"
	bob.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.newSession(t, 2, time.Hour)})
	rec = httptest.NewRecorder()
	f.anon.ServeHTTP(rec, bob)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	skipped, errs = view(rec)
	if len(skipped) != 1 || skipped[0].Owner != "Cuervos Imperiales" {
		t.Errorf("skipped = %+v, want only the Cuervos Imperiales skip", skipped)
	}
	if len(errs) != 2 || errs[0] != "Cuervos Imperiales: boom" || errs[1] != "collection failed: boom" {
		t.Errorf("errors = %q, want the corp error and the run-level error", errs)
	}
}

// The wallet links are keyed by the session user id, not the character id, so
// the status must scope with store.User.UserID: today the two are equal only
// because users.character_id is the primary key the FKs point at. A character
// skip legitimately carries a character id, so it stays compared to
// CharacterID (its owner is the signed-in character).
func TestStatusScopesByUserIDNotCharacterID(t *testing.T) {
	rep := collector.Report{
		TakenAt: base,
		Skipped: []collector.Skip{
			{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Reason: "missing scope esi-wallet.read_corporation_wallets.v1", UserID: 7},
			{OwnerKind: store.KindCorporation, OwnerID: 9, Owner: "Acme", Reason: "missing corporation role", UserID: 7},
		},
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	// The signed-in user has internal id 7 but character id 1: only user 7
	// has the wallet link that should make Acme's skip visible.
	if err := f.st.UpsertUser(context.Background(), 7, "Dana", f.clock()); err != nil {
		t.Fatal(err)
	}
	f.saveChar(t, 7, 1, "Alice")
	corpID, err := f.st.UpsertWallet(context.Background(), store.Wallet{Kind: store.KindCorporation, OwnerID: 9, OwnerName: "Acme", Division: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 7, corpID)
	s := &server{deps: Deps{Store: f.st, Status: func() StatusSnapshot { return StatusFromReport(rep) }, Now: f.clock}}
	rec := httptest.NewRecorder()
	s.status(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil), store.User{UserID: 7, CharacterID: 1})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Skipped []struct{ Owner string } `json:"skipped"`
	}
	decode(t, rec, &got)
	if len(got.Skipped) != 2 || got.Skipped[0].Owner != "Alice" || got.Skipped[1].Owner != "Acme" {
		t.Errorf("skipped = %+v, want Alice (by character id) and Acme (by user id)", got.Skipped)
	}
}

// saveChar registers a character under the user by storing its token.
func (f *fixture) saveChar(t *testing.T, userID, charID int64, name string) {
	t.Helper()
	err := f.st.SaveToken(context.Background(), store.Token{
		CharacterID: charID, UserID: userID, CharacterName: name, RefreshToken: "r-" + name, Scopes: []string{"s"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A character skip or error belongs to the user that owns the character, not
// only to its primary one: added characters count, other users' do not.
func TestStatusShowsSkipsAndErrorsOfAddedCharacters(t *testing.T) {
	rep := collector.Report{
		TakenAt: base,
		Skipped: []collector.Skip{
			{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Reason: "primary skip", UserID: 1},
			{OwnerKind: store.KindCharacter, OwnerID: 11, Owner: "Alice Alt", Reason: "added skip", UserID: 1},
			{OwnerKind: store.KindCharacter, OwnerID: 22, Owner: "Bob Alt", Reason: "other user skip", UserID: 2},
		},
		Errors: []collector.ItemError{
			{OwnerKind: store.KindCharacter, OwnerID: 11, Owner: "Alice Alt", Err: errBoom{}},
			{OwnerKind: store.KindCharacter, OwnerID: 22, Owner: "Bob Alt", Err: errBoom{}},
		},
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	f.addUser(t, 2, "Bob")
	f.saveChar(t, 1, 1, "Alice")
	f.saveChar(t, 1, 11, "Alice Alt")
	f.saveChar(t, 2, 22, "Bob Alt")

	rec := do(f.h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		Skipped []struct{ Owner, Reason string } `json:"skipped"`
		Errors  []string                         `json:"errors"`
	}
	decode(t, rec, &got)
	if len(got.Skipped) != 2 || got.Skipped[0].Reason != "primary skip" || got.Skipped[1].Reason != "added skip" {
		t.Errorf("skipped = %+v, want the primary and the added character skips only", got.Skipped)
	}
	if len(got.Errors) != 1 || got.Errors[0] != "Alice Alt: boom" {
		t.Errorf("errors = %q, want only the added character error", got.Errors)
	}
}

// A character whose collector error is auth.ErrReauthRequired is listed in
// `reauth` for its owner only; the plain `errors` strings stay unchanged.
func TestStatusExposesReauthToOwnerOnly(t *testing.T) {
	rep := collector.Report{
		TakenAt: base,
		Errors: []collector.ItemError{
			{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Err: &auth.ReauthError{CharacterID: 1, Name: "Alice"}},
			{OwnerKind: store.KindCharacter, OwnerID: 2, Owner: "Bob", Err: &auth.ReauthError{CharacterID: 2, Name: "Bob"}},
			// A run-level error that wraps Bob's reauth must not leak him to Alice.
			{Err: fmt.Errorf("refresh: %w", &auth.ReauthError{CharacterID: 2, Name: "Bob"})},
			{OwnerKind: store.KindCharacter, OwnerID: 1, Owner: "Alice", Err: errBoom{}},
		},
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	f.addUser(t, 2, "Bob")
	f.saveChar(t, 1, 1, "Alice")
	f.saveChar(t, 2, 2, "Bob")

	type reauth struct {
		CharacterID int64  `json:"character_id"`
		Name        string `json:"name"`
	}
	view := func(rec *httptest.ResponseRecorder) (r []reauth, errs []string) {
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var got struct {
			Reauth []reauth `json:"reauth"`
			Errors []string `json:"errors"`
		}
		decode(t, rec, &got)
		return got.Reauth, got.Errors
	}

	r, errs := view(do(f.h, http.MethodGet, "/api/status"))
	if len(r) != 1 || r[0].CharacterID != 1 || r[0].Name != "Alice" {
		t.Errorf("alice reauth = %+v, want only Alice", r)
	}
	if len(errs) != 3 || errs[0] != "Alice: auth: character 1 (Alice) must sign in again" || errs[1] != "refresh: auth: character 2 (Bob) must sign in again" || errs[2] != "Alice: boom" {
		t.Errorf("alice errors = %q, want the unchanged strings", errs)
	}

	bob := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	bob.Host = "localhost"
	bob.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.newSession(t, 2, time.Hour)})
	rec := httptest.NewRecorder()
	f.anon.ServeHTTP(rec, bob)
	r, _ = view(rec)
	if len(r) != 1 || r[0].CharacterID != 2 || r[0].Name != "Bob" {
		t.Errorf("bob reauth = %+v, want only Bob (once)", r)
	}
}

// With nothing to re-authenticate, reauth is an empty array, not null.
func TestStatusReauthIsEmptyArrayByDefault(t *testing.T) {
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(collector.Report{TakenAt: base}) }, false)
	rec := do(f.h, http.MethodGet, "/api/status")
	if !strings.Contains(rec.Body.String(), `"reauth":[]`) {
		t.Errorf("body = %s, want reauth:[]", rec.Body.String())
	}
}

// A character added to a user (not the one used to sign in) that must sign in
// again is reported to that user, and to nobody else.
func TestStatusReauthCoversAddedCharacterOfTheUser(t *testing.T) {
	rep := collector.Report{
		TakenAt: base,
		Errors: []collector.ItemError{
			{OwnerKind: store.KindCharacter, OwnerID: 3, Owner: "Carol", Err: &auth.ReauthError{CharacterID: 3, Name: "Carol"}},
		},
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	f.addUser(t, 2, "Bob")
	f.saveChar(t, 1, 1, "Alice") // the character user 1 signs in with
	f.saveChar(t, 1, 3, "Carol") // added later to the same user
	f.saveChar(t, 2, 2, "Bob")

	type reauth struct {
		CharacterID int64  `json:"character_id"`
		Name        string `json:"name"`
	}
	view := func(rec *httptest.ResponseRecorder) []reauth {
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		var got struct {
			Reauth []reauth `json:"reauth"`
		}
		decode(t, rec, &got)
		return got.Reauth
	}

	if r := view(do(f.h, http.MethodGet, "/api/status")); len(r) != 1 || r[0].CharacterID != 3 || r[0].Name != "Carol" {
		t.Errorf("user 1 reauth = %+v, want only the added character Carol", r)
	}

	bob := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	bob.Host = "localhost"
	bob.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.newSession(t, 2, time.Hour)})
	rec := httptest.NewRecorder()
	f.anon.ServeHTTP(rec, bob)
	if r := view(rec); len(r) != 0 {
		t.Errorf("user 2 reauth = %+v, want none", r)
	}
}
