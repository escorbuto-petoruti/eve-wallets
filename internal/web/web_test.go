package web

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

var base = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

type fixture struct {
	h              http.Handler
	st             *store.Store
	charID, corpID int64 // wallet ids
}

func newFixture(t *testing.T, status func() StatusSnapshot, seed bool) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/w.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, h: New(Deps{Store: st, Status: status})}
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
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
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
		"Content-Security-Policy": "default-src 'self'",
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

func TestWalletsWithoutBalanceHaveNullCents(t *testing.T) {
	f := newFixture(t, nil, false)
	if _, err := f.st.UpsertWallet(context.Background(), store.Wallet{Kind: store.KindCharacter, OwnerID: 5, OwnerName: "Bob"}); err != nil {
		t.Fatal(err)
	}
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
		TakenAt:     base,
		Snapshots:   make([]collector.Snapshot, 3),
		Skipped:     []collector.Skip{{Owner: "Alice", Reason: "missing corporation role"}},
		Errors:      []collector.ItemError{{Owner: "Bob", Err: errBoom{}}},
		RateLimited: true,
		RetryAfter:  90 * time.Second,
	}
	f := newFixture(t, func() StatusSnapshot { return StatusFromReport(rep) }, false)
	rec := do(f.h, http.MethodGet, "/api/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		TakenAt           *int64 `json:"taken_at"`
		Snapshots         int    `json:"snapshots"`
		Skipped           []struct{ Owner, Reason string }
		Errors            []string `json:"errors"`
		RateLimited       bool     `json:"rate_limited"`
		RetryAfterSeconds int      `json:"retry_after_seconds"`
	}
	decode(t, rec, &got)
	if got.TakenAt == nil || *got.TakenAt != base.Unix() || got.Snapshots != 3 || !got.RateLimited ||
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
	for _, want := range []string{`"taken_at":null`, `"skipped":[]`, `"errors":[]`, `"rate_limited":false`} {
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
	if body := do(f.h, http.MethodGet, "/").Body.String(); !strings.Contains(body, "eve-wallets collect") {
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
		s := string(b)
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
