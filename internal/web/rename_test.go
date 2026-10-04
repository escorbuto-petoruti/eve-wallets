package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

type renameResp struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	NameSource string `json:"name_source"`
	Error      string `json:"error"`
}

// renameReq posts a rename as a same-origin JSON request of f.aliceCookie.
func renameReq(f *fixture, walletID int64, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	return request(f.anon, http.MethodPost, "/api/wallets/"+strconv.FormatInt(walletID, 10)+"/label", func(r *http.Request) {
		r.Body = readCloser(body)
		r.ContentLength = int64(len(body))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://localhost")
		if mutate != nil {
			mutate(r)
		}
	})
}

func readCloser(s string) *nopCloser { return &nopCloser{strings.NewReader(s)} }

type nopCloser struct{ *strings.Reader }

func (nopCloser) Close() error { return nil }

func walletName(t *testing.T, f *fixture, id int64) (string, store.NameSource) {
	t.Helper()
	ws, err := f.st.WalletsForUser(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == id {
			return w.DisplayName(), w.NameSource()
		}
	}
	t.Fatalf("wallet %d not found", id)
	return "", ""
}

func TestRenameWallet(t *testing.T) {
	ctx := context.Background()

	t.Run("sets a label and returns the wallet", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.corpID, `{"name":"  Mining <b>fund</b> "}`, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body %s", rec.Code, rec.Body)
		}
		assertSecurityHeaders(t, rec, "/api/wallets/x/label")
		var got renameResp
		decode(t, rec, &got)
		if got.ID != f.corpID || got.Name != "Mining <b>fund</b>" || got.NameSource != "custom" {
			t.Errorf("response = %+v", got)
		}
		if n, src := walletName(t, f, f.corpID); n != "Mining <b>fund</b>" || src != store.NameCustom {
			t.Errorf("stored = %q %q", n, src)
		}
	})

	t.Run("empty or blank name clears the label", func(t *testing.T) {
		for _, body := range []string{`{"name":""}`, `{"name":"   "}`} {
			f := newFixture(t, nil, true)
			if err := f.st.SetLabel(ctx, f.corpID, "Old"); err != nil {
				t.Fatal(err)
			}
			rec := renameReq(f, f.corpID, body, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d body %s", body, rec.Code, rec.Body)
			}
			var got renameResp
			decode(t, rec, &got)
			if got.Name != "Division 3" || got.NameSource != "default" {
				t.Errorf("%s: response = %+v", body, got)
			}
			if n, src := walletName(t, f, f.corpID); n != "Division 3" || src != store.NameDefault {
				t.Errorf("%s: stored = %q %q", body, n, src)
			}
		}
	})

	t.Run("a custom label over an ESI name can be renamed and reset to the ESI name", func(t *testing.T) {
		f := newFixture(t, nil, true)
		if err := f.st.SetESIName(ctx, f.corpID, "Ops"); err != nil {
			t.Fatal(err)
		}
		if err := f.st.SetLabel(ctx, f.corpID, "Mine"); err != nil {
			t.Fatal(err)
		}
		rec := renameReq(f, f.corpID, `{"name":""}`, nil)
		var got renameResp
		decode(t, rec, &got)
		if rec.Code != http.StatusOK || got.Name != "Ops" || got.NameSource != "esi" {
			t.Errorf("status = %d response = %+v", rec.Code, got)
		}
	})

	t.Run("invalid names are 400", func(t *testing.T) {
		for name, body := range map[string]string{
			"too long": `{"name":"` + strings.Repeat("a", 65) + `"}`,
			"control":  `{"name":"a\u0007b"}`,
		} {
			f := newFixture(t, nil, true)
			rec := renameReq(f, f.corpID, body, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", name, rec.Code)
			}
			if _, src := walletName(t, f, f.corpID); src != store.NameDefault {
				t.Errorf("%s: label changed", name)
			}
		}
	})

	t.Run("malformed bodies are 400", func(t *testing.T) {
		for name, body := range map[string]string{
			"not json":     `name=x`,
			"missing name": `{}`,
			"wrong type":   `{"name":5}`,
			"empty body":   ``,
			"trailing":     `{"name":"a"}{"name":"b"}`,
		} {
			f := newFixture(t, nil, true)
			if rec := renameReq(f, f.corpID, body, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", name, rec.Code)
			}
		}
	})

	t.Run("oversized body is refused", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.corpID, `{"name":"`+strings.Repeat("a", 5000)+`"}`, nil)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 400 or 413", rec.Code)
		}
		if _, src := walletName(t, f, f.corpID); src != store.NameDefault {
			t.Error("label changed")
		}
	})

	t.Run("division 1 is forbidden", func(t *testing.T) {
		f := newFixture(t, nil, true)
		id, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 9, OwnerName: "Corp", Division: 1})
		if err != nil {
			t.Fatal(err)
		}
		f.link(t, 1, id)
		rec := renameReq(f, id, `{"name":"X"}`, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		var got renameResp
		decode(t, rec, &got)
		if !strings.Contains(got.Error, "Master Wallet") {
			t.Errorf("error = %q", got.Error)
		}
		if n, _ := walletName(t, f, id); n != "Master Wallet" {
			t.Errorf("name = %q", n)
		}
	})

	t.Run("esi-named wallet is forbidden", func(t *testing.T) {
		f := newFixture(t, nil, true)
		if err := f.st.SetESIName(ctx, f.corpID, "Ops"); err != nil {
			t.Fatal(err)
		}
		rec := renameReq(f, f.corpID, `{"name":"X"}`, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if n, src := walletName(t, f, f.corpID); n != "Ops" || src != store.NameESI {
			t.Errorf("stored = %q %q", n, src)
		}
	})

	t.Run("personal wallet is forbidden", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.charID, `{"name":"X"}`, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if _, src := walletName(t, f, f.charID); src != store.NameDefault {
			t.Error("label changed")
		}
	})

	t.Run("another user's or unknown wallet is 404", func(t *testing.T) {
		f := newFixture(t, nil, true)
		other, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: 77, OwnerName: "Other", Division: 2})
		if err != nil {
			t.Fatal(err)
		}
		f.addUser(t, 2, "Bob")
		f.link(t, 2, other)
		for _, id := range []int64{other, 9999} {
			if rec := renameReq(f, id, `{"name":"X"}`, nil); rec.Code != http.StatusNotFound {
				t.Errorf("wallet %d: status = %d, want 404", id, rec.Code)
			}
		}
		ws, err := f.st.Wallets(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range ws {
			if w.ID == other && w.Label != "" {
				t.Error("another user's wallet was renamed")
			}
		}
	})

	t.Run("a bad wallet id is 404", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := request(f.anon, http.MethodPost, "/api/wallets/abc/label", func(r *http.Request) {
			r.Body = readCloser(`{"name":"X"}`)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.aliceCookie})
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "http://localhost")
		})
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("no session is 401", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.corpID, `{"name":"X"}`, func(r *http.Request) { r.Header.Del("Cookie") })
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
		if _, src := walletName(t, f, f.corpID); src != store.NameDefault {
			t.Error("label changed")
		}
	})

	t.Run("wrong content type is 415", func(t *testing.T) {
		for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "", "application/jsonx"} {
			f := newFixture(t, nil, true)
			rec := renameReq(f, f.corpID, `{"name":"X"}`, func(r *http.Request) {
				if ct == "" {
					r.Header.Del("Content-Type")
				} else {
					r.Header.Set("Content-Type", ct)
				}
			})
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%q: status = %d, want 415", ct, rec.Code)
			}
			if _, src := walletName(t, f, f.corpID); src != store.NameDefault {
				t.Errorf("%q: label changed", ct)
			}
		}
	})

	t.Run("json content type with charset is accepted", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.corpID, `{"name":"X"}`, func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		})
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d", rec.Code)
		}
	})
}

func TestRenameWalletRequiresSameOrigin(t *testing.T) {
	refused := map[string]func(*http.Request){
		"cross origin":        func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") },
		"no origin evidence":  func(r *http.Request) { r.Header.Del("Origin") },
		"origin null":         func(r *http.Request) { r.Header.Set("Origin", "null") },
		"cross-site metadata": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site metadata":  func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"cross-site beats origin": func(r *http.Request) {
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		},
	}
	for name, mutate := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			f := newFixture(t, nil, true)
			rec := renameReq(f, f.corpID, `{"name":"X"}`, mutate)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if _, src := walletName(t, f, f.corpID); src != store.NameDefault {
				t.Error("a refused request changed the label")
			}
		})
	}
	t.Run("accepts Origin null with Sec-Fetch-Site same-origin", func(t *testing.T) {
		f := newFixture(t, nil, true)
		rec := renameReq(f, f.corpID, `{"name":"X"}`, func(r *http.Request) {
			r.Header.Set("Origin", "null")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
		})
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rec.Code)
		}
	})
}

func TestRenameRouteMethods(t *testing.T) {
	f := newFixture(t, nil, true)
	target := "/api/wallets/" + strconv.FormatInt(f.corpID, 10) + "/label"
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := request(f.h, m, target, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, rec.Code)
		}
	}
	// POST to the other API routes stays refused.
	for _, p := range []string{"/api/wallets", "/api/wallets/1", "/api/series", "/api/wallets/1/label/x"} {
		rec := request(f.h, http.MethodPost, p, func(r *http.Request) { r.Header.Set("Origin", "http://localhost") })
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST %s status = %d, want 405 or 404", p, rec.Code)
		}
	}
}
