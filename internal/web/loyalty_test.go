package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const lpScope = "esi-characters.read_loyalty.v1"

type loyaltyBody struct {
	Characters []struct {
		CharacterID   int64  `json:"character_id"`
		CharacterName string `json:"character_name"`
		FetchedAt     *int64 `json:"fetched_at"`
		NeedsReauth   bool   `json:"needs_reauth"`
		Corporations  []struct {
			CorporationID int64  `json:"corporation_id"`
			Name          string `json:"name"`
			Points        int64  `json:"points"`
		} `json:"corporations"`
	} `json:"characters"`
}

// seedToken registers a character of user with the given scopes.
func (f *fixture) seedToken(t *testing.T, user, char int64, name string, scopes ...string) {
	t.Helper()
	err := f.st.SaveToken(context.Background(), store.Token{CharacterID: char, UserID: user, CharacterName: name, RefreshToken: "r", Scopes: scopes})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLoyaltyListsOwnCharactersSortedWithNames(t *testing.T) {
	f := newFixture(t, nil, false)
	ctx := context.Background()
	f.seedToken(t, 1, 1, "Alice", lpScope)
	f.seedToken(t, 1, 5, "Alice Alt", "esi-wallet.read_character_wallet.v1")
	f.seedToken(t, 1, 6, "Empty", lpScope)
	f.addUser(t, 2, "Bob")
	f.seedToken(t, 2, 2, "Bob", lpScope)
	at := base.Add(time.Hour)
	if err := f.st.ReplaceLoyalty(ctx, 1, []store.LoyaltyPoints{
		{CorporationID: 11, Points: 50}, {CorporationID: 12, Points: 9007199254740993}, {CorporationID: 13, Points: 700}}, at); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceLoyalty(ctx, 2, []store.LoyaltyPoints{{CorporationID: 99, Points: 1}}, at); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertCorporationNames(ctx, map[int64]string{11: "Alpha <b>", 12: "Beta"}, at); err != nil {
		t.Fatal(err)
	}

	rec := do(f.h, http.MethodGet, "/api/loyalty")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	assertSecurityHeaders(t, rec, "/api/loyalty")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	var body loyaltyBody
	decode(t, rec, &body)
	if len(body.Characters) != 3 {
		t.Fatalf("characters = %+v, want Alice, her alt and Empty only", body.Characters)
	}
	alice, alt, empty := body.Characters[0], body.Characters[1], body.Characters[2]
	if alice.CharacterID != 1 || alice.CharacterName != "Alice" || alice.NeedsReauth || alice.FetchedAt == nil || *alice.FetchedAt != at.Unix() {
		t.Fatalf("alice = %+v", alice)
	}
	if len(alice.Corporations) != 3 || alice.Corporations[0].CorporationID != 12 || alice.Corporations[0].Points != 9007199254740993 ||
		alice.Corporations[1].Points != 700 || alice.Corporations[2].Points != 50 {
		t.Fatalf("alice corporations = %+v, want points descending", alice.Corporations)
	}
	if alice.Corporations[0].Name != "Beta" || alice.Corporations[2].Name != "Alpha <b>" {
		t.Fatalf("names = %+v", alice.Corporations)
	}
	if alice.Corporations[1].Name != "Corp 13" {
		t.Fatalf("unknown name = %q, want a printable id fallback", alice.Corporations[1].Name)
	}
	if alt.CharacterID != 5 || !alt.NeedsReauth || alt.Corporations == nil || len(alt.Corporations) != 0 {
		t.Fatalf("alt = %+v, want needs_reauth with an empty list", alt)
	}
	if empty.NeedsReauth || empty.FetchedAt != nil || empty.Corporations == nil || len(empty.Corporations) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
	if strings.Contains(rec.Body.String(), "Bob") || strings.Contains(rec.Body.String(), `"corporation_id":99`) {
		t.Fatalf("another user's character leaked: %s", rec.Body)
	}
}

func TestLoyaltyWithoutCharactersIsEmptyArray(t *testing.T) {
	f := newFixture(t, nil, false)
	rec := do(f.h, http.MethodGet, "/api/loyalty")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"characters":[]}` {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
}

func TestLoyaltyRequiresASessionAndOnlyAllowsGet(t *testing.T) {
	f := newFixture(t, nil, false)
	if rec := do(f.anon, http.MethodGet, "/api/loyalty"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := do(f.h, m, "/api/loyalty")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s status = %d, Allow = %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	rec := do(f.h, http.MethodHead, "/api/loyalty")
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}
}

type historyBody struct {
	CharacterID int64 `json:"character_id"`
	Points      []struct {
		CorporationID int64  `json:"corporation_id"`
		Name          string `json:"name"`
		TakenAt       int64  `json:"taken_at"`
		Points        int64  `json:"points"`
	} `json:"points"`
	NextCursor *string `json:"next_cursor"`
}

// seedHistory stores three collections for character 1 (user 1) and one for
// Bob's character 2.
func seedHistory(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	f.seedToken(t, 1, 1, "Alice", lpScope)
	f.addUser(t, 2, "Bob")
	f.seedToken(t, 2, 2, "Bob", lpScope)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }
	steps := []struct {
		m    int
		rows []store.LoyaltyPoints
	}{
		{0, []store.LoyaltyPoints{{CorporationID: 11, Points: 10}, {CorporationID: 12, Points: 20}}},
		{10, []store.LoyaltyPoints{{CorporationID: 11, Points: 15}, {CorporationID: 12, Points: 20}}},
		{20, []store.LoyaltyPoints{{CorporationID: 11, Points: 15}}},
	}
	for _, s := range steps {
		if err := f.st.ReplaceLoyalty(ctx, 1, s.rows, at(s.m)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.ReplaceLoyalty(ctx, 2, []store.LoyaltyPoints{{CorporationID: 99, Points: 1}}, at(0)); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpsertCorporationNames(ctx, map[int64]string{11: "Alpha <b>"}, at(0)); err != nil {
		t.Fatal(err)
	}
}

func TestLoyaltyHistoryListsOwnCharacterAscendingWithNames(t *testing.T) {
	f := newFixture(t, nil, false)
	seedHistory(t, f)
	rec := do(f.h, http.MethodGet, "/api/loyalty/history?character_id=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	assertSecurityHeaders(t, rec, "/api/loyalty/history")
	var body historyBody
	decode(t, rec, &body)
	if body.CharacterID != 1 || body.NextCursor != nil {
		t.Fatalf("body = %+v", body)
	}
	type row struct{ corp, at, pts int64 }
	var got []row
	for _, p := range body.Points {
		got = append(got, row{p.CorporationID, p.TakenAt, p.Points})
	}
	b := base.Unix()
	want := []row{{11, b, 10}, {12, b, 20}, {11, b + 600, 15}, {12, b + 1200, 0}}
	if len(got) != len(want) {
		t.Fatalf("points = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("points = %+v, want %+v", got, want)
		}
	}
	if body.Points[0].Name != "Alpha <b>" || body.Points[1].Name != "Corp 12" {
		t.Fatalf("names = %q, %q", body.Points[0].Name, body.Points[1].Name)
	}
	if strings.Contains(rec.Body.String(), "Bob") || strings.Contains(rec.Body.String(), `"corporation_id":99`) {
		t.Fatalf("another user's data leaked: %s", rec.Body)
	}
}

func TestLoyaltyHistoryFilters(t *testing.T) {
	f := newFixture(t, nil, false)
	seedHistory(t, f)
	count := func(q string) int {
		t.Helper()
		rec := do(f.h, http.MethodGet, "/api/loyalty/history?character_id=1&"+q)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d: %s", q, rec.Code, rec.Body)
		}
		var body historyBody
		decode(t, rec, &body)
		return len(body.Points)
	}
	from := base.Add(10 * time.Minute).Format(time.RFC3339)
	to := base.Add(10 * time.Minute).Format(time.RFC3339)
	if n := count("corporation_id=11"); n != 2 {
		t.Errorf("corporation filter = %d, want 2", n)
	}
	if n := count("from=" + from); n != 2 {
		t.Errorf("from = %d, want 2 (inclusive)", n)
	}
	if n := count("to=" + to); n != 3 {
		t.Errorf("to = %d, want 3 (inclusive)", n)
	}
	if n := count("corporation_id=777"); n != 0 {
		t.Errorf("unknown corporation = %d, want 0", n)
	}
	rec := do(f.h, http.MethodGet, "/api/loyalty/history?character_id=1&corporation_id=777")
	if !strings.Contains(rec.Body.String(), `"points":[]`) || !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("empty result = %s, want [] and null", rec.Body)
	}
}

func TestLoyaltyHistoryPagesWithAKeysetCursor(t *testing.T) {
	f := newFixture(t, nil, false)
	seedHistory(t, f)
	var seen []int64
	cursor := ""
	for page := 0; page < 10; page++ {
		url := "/api/loyalty/history?character_id=1&limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		rec := do(f.h, http.MethodGet, url)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body)
		}
		var body historyBody
		decode(t, rec, &body)
		if len(body.Points) > 3 {
			t.Fatalf("page of %d rows exceeds the limit", len(body.Points))
		}
		for _, p := range body.Points {
			seen = append(seen, p.TakenAt*1000+p.CorporationID)
		}
		if body.NextCursor == nil {
			break
		}
		cursor = *body.NextCursor
	}
	if len(seen) != 4 {
		t.Fatalf("paged rows = %v, want 4 without repeats", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("rows not strictly ascending: %v", seen)
		}
	}
}

func TestLoyaltyHistoryRejectsBadAndForeignRequests(t *testing.T) {
	f := newFixture(t, nil, false)
	seedHistory(t, f)
	for target, want := range map[string]int{
		"/api/loyalty/history":                                                                  http.StatusBadRequest,
		"/api/loyalty/history?character_id=abc":                                                 http.StatusBadRequest,
		"/api/loyalty/history?character_id=0":                                                   http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&corporation_id=x":                                  http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&from=yesterday":                                    http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&to=2020-13-01":                                     http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&limit=0":                                           http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&limit=2001":                                        http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&cursor=nope":                                       http.StatusBadRequest,
		"/api/loyalty/history?character_id=1&from=2021-01-02T00:00:00Z&to=2021-01-01T00:00:00Z": http.StatusBadRequest,
		"/api/loyalty/history?character_id=2":                                                   http.StatusNotFound,
		"/api/loyalty/history?character_id=424242":                                              http.StatusNotFound,
	} {
		if rec := do(f.h, http.MethodGet, target); rec.Code != want {
			t.Errorf("%s: status = %d, want %d (%s)", target, rec.Code, want, rec.Body)
		}
	}
}

func TestLoyaltyHistoryRequiresASessionAndOnlyAllowsGet(t *testing.T) {
	f := newFixture(t, nil, false)
	seedHistory(t, f)
	if rec := do(f.anon, http.MethodGet, "/api/loyalty/history?character_id=1"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := do(f.h, m, "/api/loyalty/history?character_id=1")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s status = %d, Allow = %q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
}
