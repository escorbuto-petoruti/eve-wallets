package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

type journalResp struct {
	Entries []struct {
		ID          int64  `json:"id"`
		Date        int64  `json:"date"`
		Cents       int64  `json:"cents"`
		RefType     string `json:"ref_type"`
		Description string `json:"description"`
	} `json:"entries"`
	NextCursor *string  `json:"next_cursor"`
	RefTypes   []string `json:"ref_types"`
	Error      string   `json:"error"`
}

func (j journalResp) ids() []int64 {
	out := []int64{}
	for _, e := range j.Entries {
		out = append(out, e.ID)
	}
	return out
}

func seedJournal(t *testing.T, f *fixture) {
	t.Helper()
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	_, err := f.st.AddJournalEntries(context.Background(), f.charID, []store.JournalEntry{
		{ID: 1, Date: at(1), AmountCents: 100, RefType: "bounty_prizes", Description: "<b>one</b>"},
		{ID: 2, Date: at(2), AmountCents: -250, RefType: "market_fee", Description: "two"},
		{ID: 3, Date: at(2), AmountCents: 300, RefType: "bounty_prizes", Description: "three"},
		{ID: 4, Date: at(3), AmountCents: 400, RefType: "player_trading", Description: "four"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func journalGet(f *fixture, id int64, q url.Values) (*journalResp, int, http.Header) {
	target := "/api/wallets/" + itoa(id) + "/journal"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	rec := do(f.h, http.MethodGet, target)
	var out journalResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return &out, rec.Code, rec.Header()
}

func TestJournalEndpointReturnsNewestFirstWithTypes(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	rec := do(f.h, http.MethodGet, "/api/wallets/"+itoa(f.charID)+"/journal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	assertSecurityHeaders(t, rec, "/api/wallets/x/journal")
	var got journalResp
	decode(t, rec, &got)
	if !reflect.DeepEqual(got.ids(), []int64{4, 3, 2, 1}) || got.NextCursor != nil {
		t.Fatalf("response = %+v", got)
	}
	e := got.Entries[2]
	if e.Cents != -250 || e.RefType != "market_fee" || e.Description != "two" || e.Date != base.Add(2*time.Hour).Unix() {
		t.Fatalf("entry = %+v", e)
	}
	if !reflect.DeepEqual(got.RefTypes, []string{"bounty_prizes", "market_fee", "player_trading"}) {
		t.Fatalf("ref types = %v", got.RefTypes)
	}
}

func TestJournalEndpointEmptyWalletHasEmptyArrays(t *testing.T) {
	f := newFixture(t, nil, true)
	rec := do(f.h, http.MethodGet, "/api/wallets/"+itoa(f.corpID)+"/journal")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if want := `{"entries":[],"next_cursor":null,"ref_types":[]}`; body != want+"\n" && body != want {
		t.Fatalf("body = %q", body)
	}
}

func TestJournalEndpointKeysetPaging(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	p1, code, _ := journalGet(f, f.charID, url.Values{"limit": {"3"}})
	if code != http.StatusOK || !reflect.DeepEqual(p1.ids(), []int64{4, 3, 2}) || p1.NextCursor == nil {
		t.Fatalf("page 1 = %+v (%d)", p1, code)
	}
	// An entry that arrives between the two requests must not shift page 2.
	if _, err := f.st.AddJournalEntries(context.Background(), f.charID, []store.JournalEntry{
		{ID: 5, Date: base.Add(10 * time.Hour), AmountCents: 1, RefType: "x", Description: "new"},
	}); err != nil {
		t.Fatal(err)
	}
	p2, code, _ := journalGet(f, f.charID, url.Values{"limit": {"3"}, "cursor": {*p1.NextCursor}})
	if code != http.StatusOK || !reflect.DeepEqual(p2.ids(), []int64{1}) || p2.NextCursor != nil {
		t.Fatalf("page 2 = %+v (%d)", p2, code)
	}
	// A page that ends exactly at the last row has no next cursor.
	exact, _, _ := journalGet(f, f.charID, url.Values{"limit": {"5"}})
	if len(exact.Entries) != 5 || exact.NextCursor != nil {
		t.Fatalf("exact = %+v", exact)
	}
}

func TestJournalEndpointFilters(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	byType, code, _ := journalGet(f, f.charID, url.Values{"ref_type": {"bounty_prizes"}})
	if code != http.StatusOK || !reflect.DeepEqual(byType.ids(), []int64{3, 1}) {
		t.Fatalf("by type = %v (%d)", byType.ids(), code)
	}
	if len(byType.RefTypes) != 3 {
		t.Fatalf("ref types must list every type of the wallet, got %v", byType.RefTypes)
	}
	from := base.Add(2 * time.Hour).Format(time.RFC3339)
	to := base.Add(2 * time.Hour).Format(time.RFC3339)
	ranged, _, _ := journalGet(f, f.charID, url.Values{"from": {from}, "to": {to}})
	if !reflect.DeepEqual(ranged.ids(), []int64{3, 2}) {
		t.Fatalf("range = %v", ranged.ids())
	}
	unknown, code, _ := journalGet(f, f.charID, url.Values{"ref_type": {"nope"}})
	if code != http.StatusOK || len(unknown.Entries) != 0 {
		t.Fatalf("unknown type = %+v (%d)", unknown, code)
	}
}

func TestJournalEndpointRejectsInvalidParams(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	for name, q := range map[string]url.Values{
		"limit zero":      {"limit": {"0"}},
		"limit negative":  {"limit": {"-1"}},
		"limit too big":   {"limit": {"201"}},
		"limit text":      {"limit": {"x"}},
		"cursor garbage":  {"cursor": {"nope"}},
		"cursor negative": {"cursor": {"1-"}},
		"from garbage":    {"from": {"yesterday"}},
		"to garbage":      {"to": {"2026-13-40"}},
		"to before from":  {"from": {"2026-05-02T00:00:00Z"}, "to": {"2026-05-01T00:00:00Z"}},
		"ref type long":   {"ref_type": {string(make([]byte, 200))}},
	} {
		got, code, _ := journalGet(f, f.charID, q)
		if code != http.StatusBadRequest || got.Error == "" {
			t.Errorf("%s: status = %d, error = %q", name, code, got.Error)
		}
	}
	if got, code, _ := journalGet(f, f.charID, url.Values{"limit": {"200"}}); code != http.StatusOK || len(got.Entries) != 4 {
		t.Errorf("limit 200 = %d %+v", code, got)
	}
}

func TestJournalEndpointNotFoundForUnknownOrForeignWallet(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	ctx := context.Background()
	f.addUser(t, 2, "Bob")
	bobWallet, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 2, OwnerName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 2, bobWallet)
	if _, err := f.st.AddJournalEntries(ctx, bobWallet, []store.JournalEntry{{ID: 9, Date: base, AmountCents: 9, RefType: "secret", Description: "s"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{itoa(bobWallet), "99999", "0", "-3", "abc"} {
		rec := do(f.h, http.MethodGet, "/api/wallets/"+id+"/journal")
		if rec.Code != http.StatusNotFound {
			t.Errorf("wallet %s: status = %d", id, rec.Code)
		}
		assertSecurityHeaders(t, rec, "/api/wallets/"+id+"/journal")
	}
}

func TestJournalEndpointNeedsSessionAndOnlyAllowsGET(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	target := "/api/wallets/" + itoa(f.charID) + "/journal"
	if rec := request(f.anon, http.MethodGet, target, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d", rec.Code)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := do(f.h, m, target)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d", m, rec.Code)
		}
	}
}
