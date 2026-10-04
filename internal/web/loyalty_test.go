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
