package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
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

type dailyResp struct {
	Days []struct {
		Day          string `json:"day"`
		IncomeCents  int64  `json:"income_cents"`
		ExpenseCents int64  `json:"expense_cents"`
	} `json:"days"`
	Error string `json:"error"`
}

func dailyGet(f *fixture, id string, q url.Values) (*dailyResp, int, string) {
	target := "/api/wallets/" + id + "/journal/daily"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	rec := do(f.h, http.MethodGet, target)
	var out dailyResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return &out, rec.Code, rec.Body.String()
}

func (d dailyResp) rows() [][3]any {
	out := [][3]any{}
	for _, x := range d.Days {
		out = append(out, [3]any{x.Day, x.IncomeCents, x.ExpenseCents})
	}
	return out
}

func addJournal(t *testing.T, f *fixture, es ...store.JournalEntry) {
	t.Helper()
	if _, err := f.st.AddJournalEntries(context.Background(), f.charID, es); err != nil {
		t.Fatal(err)
	}
}

func TestJournalDailyGroupsZeroFillsAndSplitsIncomeFromExpenses(t *testing.T) {
	f := newFixture(t, nil, true)
	d := func(day, h int) time.Time { return time.Date(2026, 4, day, h, 0, 0, 0, time.UTC) }
	addJournal(t, f,
		store.JournalEntry{ID: 1, Date: d(27, 8), AmountCents: 100, RefType: "bounty_prizes"},
		store.JournalEntry{ID: 2, Date: d(27, 9), AmountCents: -30, RefType: "market_fee"},
		store.JournalEntry{ID: 3, Date: d(27, 10), AmountCents: 50, RefType: "bounty_prizes"},
		store.JournalEntry{ID: 4, Date: d(30, 23), AmountCents: -7, RefType: "market_fee"},
	)
	got, code, body := dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, body)
	}
	want := [][3]any{
		{"2026-04-27", int64(150), int64(30)},
		{"2026-04-28", int64(0), int64(0)},
		{"2026-04-29", int64(0), int64(0)},
		{"2026-04-30", int64(0), int64(7)},
	}
	if !reflect.DeepEqual(got.rows(), want) {
		t.Fatalf("days = %v, want %v", got.rows(), want)
	}
	// The time zone moves a late-evening entry onto the next local day.
	got, _, _ = dailyGet(f, itoa(f.charID), url.Values{"tz": {"Asia/Tokyo"}})
	if len(got.Days) == 0 || got.Days[len(got.Days)-1].Day != "2026-05-01" {
		t.Fatalf("tokyo days = %v", got.rows())
	}
}

func TestJournalDailyHandlesDaylightSavingDay(t *testing.T) {
	f := newFixture(t, nil, true)
	// US spring forward 2026-03-08 in New York: that local day has 23 hours.
	// 04:59Z is still 23:59 on the 7th; 05:00Z starts the 8th; 03:59Z next day
	// (2026-03-09 03:59Z = 23:59 EDT on the 8th) still belongs to the 8th.
	at := func(day, h, m int) time.Time { return time.Date(2026, 3, day, h, m, 0, 0, time.UTC) }
	f.now.Store(at(10, 0, 0).Unix())
	addJournal(t, f,
		store.JournalEntry{ID: 1, Date: at(8, 4, 59), AmountCents: 1, RefType: "a"},
		store.JournalEntry{ID: 2, Date: at(8, 5, 0), AmountCents: 10, RefType: "a"},
		store.JournalEntry{ID: 3, Date: at(9, 3, 59), AmountCents: 100, RefType: "a"},
		store.JournalEntry{ID: 4, Date: at(9, 4, 0), AmountCents: 1000, RefType: "a"},
	)
	got, code, body := dailyGet(f, itoa(f.charID), url.Values{"tz": {"America/New_York"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, body)
	}
	want := [][3]any{
		{"2026-03-07", int64(1), int64(0)},
		{"2026-03-08", int64(110), int64(0)},
		{"2026-03-09", int64(1000), int64(0)},
	}
	if !reflect.DeepEqual(got.rows(), want) {
		t.Fatalf("days = %v, want %v", got.rows(), want)
	}
}

func TestJournalDailyFilters(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	byType, code, _ := dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}, "ref_type": {"market_fee"}})
	if code != http.StatusOK || !reflect.DeepEqual(byType.rows(), [][3]any{{"2026-05-01", int64(0), int64(250)}}) {
		t.Fatalf("by type = %v (%d)", byType.rows(), code)
	}
	from := base.Add(2 * time.Hour).Format(time.RFC3339)
	ranged, _, _ := dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}, "from": {from}, "to": {from}})
	if !reflect.DeepEqual(ranged.rows(), [][3]any{{"2026-05-01", int64(300), int64(250)}}) {
		t.Fatalf("range = %v", ranged.rows())
	}
}

func TestJournalDailyDefaultsToLast30Days(t *testing.T) {
	f := newFixture(t, nil, true)
	now := f.clock()
	addJournal(t, f,
		store.JournalEntry{ID: 1, Date: now.Add(-31 * 24 * time.Hour), AmountCents: 5, RefType: "a"},
		store.JournalEntry{ID: 2, Date: now.Add(-29 * 24 * time.Hour), AmountCents: 7, RefType: "a"},
	)
	got, code, _ := dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}})
	if code != http.StatusOK || len(got.Days) != 1 || got.Days[0].IncomeCents != 7 {
		t.Fatalf("default window = %v (%d)", got.rows(), code)
	}
}

// With only to given, the window is the 30 days before to, not all history.
func TestJournalDailyBoundsWindowWhenOnlyToIsGiven(t *testing.T) {
	f := newFixture(t, nil, true)
	to := f.clock().Add(-60 * 24 * time.Hour)
	addJournal(t, f,
		store.JournalEntry{ID: 1, Date: to.Add(-31 * 24 * time.Hour), AmountCents: 5, RefType: "a"},
		store.JournalEntry{ID: 2, Date: to.Add(-29 * 24 * time.Hour), AmountCents: 7, RefType: "a"},
		store.JournalEntry{ID: 3, Date: to.Add(-1 * time.Hour), AmountCents: 11, RefType: "a"},
	)
	got, code, body := dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}, "to": {to.Format(time.RFC3339)}})
	if code != http.StatusOK {
		t.Fatalf("status = %d body %s", code, body)
	}
	var income int64
	for _, d := range got.Days {
		income += d.IncomeCents
	}
	if income != 18 {
		t.Fatalf("income = %d (days %v), want 18: only rows within 30 days before to", income, got.rows())
	}
	// An explicit from is not capped.
	from := to.Add(-90 * 24 * time.Hour)
	got, _, _ = dailyGet(f, itoa(f.charID), url.Values{"tz": {"UTC"}, "from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}})
	income = 0
	for _, d := range got.Days {
		income += d.IncomeCents
	}
	if income != 23 {
		t.Fatalf("explicit from income = %d, want 23", income)
	}
}

func TestJournalDailyEmptyAndDefaultTimeZone(t *testing.T) {
	f := newFixture(t, nil, true)
	rec := do(f.h, http.MethodGet, "/api/wallets/"+itoa(f.corpID)+"/journal/daily")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); body != `{"days":[]}`+"\n" && body != `{"days":[]}` {
		t.Fatalf("body = %q", body)
	}
}

func TestJournalDailyRejectsInvalidParams(t *testing.T) {
	f := newFixture(t, nil, true)
	seedJournal(t, f)
	for name, q := range map[string]url.Values{
		"tz unknown":     {"tz": {"Mars/Base"}},
		"tz path":        {"tz": {"../etc/passwd"}},
		"from garbage":   {"from": {"yesterday"}},
		"to before from": {"from": {"2026-05-02T00:00:00Z"}, "to": {"2026-05-01T00:00:00Z"}},
		"ref type long":  {"ref_type": {string(make([]byte, 200))}},
	} {
		got, code, _ := dailyGet(f, itoa(f.charID), q)
		if code != http.StatusBadRequest || got.Error == "" {
			t.Errorf("%s: status = %d, error = %q", name, code, got.Error)
		}
	}
}

func TestJournalDailyNotFoundForUnknownOrForeignWallet(t *testing.T) {
	f := newFixture(t, nil, true)
	ctx := context.Background()
	f.addUser(t, 2, "Bob")
	bobWallet, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 2, OwnerName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 2, bobWallet)
	for _, id := range []string{itoa(bobWallet), "99999", "0", "abc"} {
		if _, code, _ := dailyGet(f, id, url.Values{"tz": {"UTC"}}); code != http.StatusNotFound {
			t.Errorf("wallet %s: status = %d", id, code)
		}
	}
	target := "/api/wallets/" + itoa(f.charID) + "/journal/daily"
	if rec := request(f.anon, http.MethodGet, target, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d", rec.Code)
	}
}

type multiEntry struct {
	WalletID int64  `json:"wallet_id"`
	ID       int64  `json:"id"`
	Cents    int64  `json:"cents"`
	RefType  string `json:"ref_type"`
}

type multiJournalResp struct {
	Entries    []multiEntry `json:"entries"`
	NextCursor *string      `json:"next_cursor"`
	RefTypes   []string     `json:"ref_types"`
	Error      string       `json:"error"`
}

func (m multiJournalResp) keys() [][2]int64 {
	out := [][2]int64{}
	for _, e := range m.Entries {
		out = append(out, [2]int64{e.WalletID, e.ID})
	}
	return out
}

func multiGet(f *fixture, path string, q url.Values) (int, string) {
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	rec := do(f.h, http.MethodGet, path)
	return rec.Code, rec.Body.String()
}

func ids2(f *fixture) string { return itoa(f.charID) + "," + itoa(f.corpID) }

// seedMulti stores entries whose ids collide across the two visible wallets.
func seedMulti(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	if _, err := f.st.AddJournalEntries(ctx, f.charID, []store.JournalEntry{
		{ID: 1, Date: at(1), AmountCents: 100, RefType: "bounty_prizes", Description: "a1"},
		{ID: 2, Date: at(2), AmountCents: -250, RefType: "market_fee", Description: "a2"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddJournalEntries(ctx, f.corpID, []store.JournalEntry{
		{ID: 1, Date: at(1), AmountCents: 7, RefType: "corp_tax", Description: "b1"},
		{ID: 2, Date: at(2), AmountCents: 40, RefType: "market_fee", Description: "b2"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJournalAcrossWalletsPagesWithoutSkippingOrRepeating(t *testing.T) {
	f := newFixture(t, nil, true)
	seedMulti(t, f)
	code, body := multiGet(f, "/api/journal", url.Values{"wallet_ids": {ids2(f)}})
	var all multiJournalResp
	_ = json.Unmarshal([]byte(body), &all)
	want := [][2]int64{{f.corpID, 2}, {f.charID, 2}, {f.corpID, 1}, {f.charID, 1}}
	if f.corpID < f.charID {
		want = [][2]int64{{f.charID, 2}, {f.corpID, 2}, {f.charID, 1}, {f.corpID, 1}}
	}
	if code != http.StatusOK || !reflect.DeepEqual(all.keys(), want) || all.NextCursor != nil {
		t.Fatalf("all = %d %s", code, body)
	}
	if !reflect.DeepEqual(all.RefTypes, []string{"bounty_prizes", "corp_tax", "market_fee"}) {
		t.Fatalf("ref types = %v", all.RefTypes)
	}
	var got [][2]int64
	cursor := ""
	for range 5 {
		q := url.Values{"wallet_ids": {ids2(f)}, "limit": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		code, body := multiGet(f, "/api/journal", q)
		var page multiJournalResp
		_ = json.Unmarshal([]byte(body), &page)
		if code != http.StatusOK {
			t.Fatalf("page status = %d %s", code, body)
		}
		got = append(got, page.keys()...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paged = %v, want %v", got, want)
	}
	e := all.Entries[0]
	if e.WalletID == 0 || e.Cents == 0 || e.RefType == "" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestJournalAcrossWalletsFilters(t *testing.T) {
	f := newFixture(t, nil, true)
	seedMulti(t, f)
	var r multiJournalResp
	_, body := multiGet(f, "/api/journal", url.Values{"wallet_ids": {ids2(f)}, "ref_type": {"market_fee"}})
	_ = json.Unmarshal([]byte(body), &r)
	if len(r.Entries) != 2 || len(r.RefTypes) != 3 {
		t.Fatalf("by type = %s", body)
	}
	at := base.Add(time.Hour).Format(time.RFC3339)
	_, body = multiGet(f, "/api/journal", url.Values{"wallet_ids": {ids2(f)}, "from": {at}, "to": {at}})
	r = multiJournalResp{}
	_ = json.Unmarshal([]byte(body), &r)
	if len(r.Entries) != 2 || r.Entries[0].ID != 1 {
		t.Fatalf("range = %s", body)
	}
	// A subset of the wallets only lists its own types.
	_, body = multiGet(f, "/api/journal", url.Values{"wallet_ids": {itoa(f.corpID)}})
	r = multiJournalResp{}
	_ = json.Unmarshal([]byte(body), &r)
	if !reflect.DeepEqual(r.RefTypes, []string{"corp_tax", "market_fee"}) || len(r.Entries) != 2 {
		t.Fatalf("subset = %s", body)
	}
}

func TestMultiWalletJournalRejectsForeignAndInvalidIDs(t *testing.T) {
	f := newFixture(t, nil, true)
	seedMulti(t, f)
	ctx := context.Background()
	f.addUser(t, 2, "Bob")
	bob, err := f.st.UpsertWallet(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: 2, OwnerName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	f.link(t, 2, bob)
	tooMany := make([]string, maxWalletIDs+1)
	for i := range tooMany {
		tooMany[i] = itoa(int64(i + 1))
	}
	for _, path := range []string{"/api/journal", "/api/journal/daily"} {
		for name, ids := range map[string]string{
			"foreign among visible": itoa(f.charID) + "," + itoa(bob),
			"foreign only":          itoa(bob),
			"unknown":               itoa(f.charID) + ",99999",
		} {
			if code, body := multiGet(f, path, url.Values{"wallet_ids": {ids}}); code != http.StatusNotFound {
				t.Errorf("%s %s: status = %d %s", path, name, code, body)
			}
		}
		for name, q := range map[string]url.Values{
			"missing":   {},
			"empty":     {"wallet_ids": {""}},
			"text":      {"wallet_ids": {"a,b"}},
			"zero":      {"wallet_ids": {"0"}},
			"too many":  {"wallet_ids": {strings.Join(tooMany, ",")}},
			"bad limit": {"wallet_ids": {ids2(f)}, "limit": {"0"}},
			"bad tz":    {"wallet_ids": {ids2(f)}, "tz": {"Nope/Zone"}},
		} {
			if path == "/api/journal/daily" && name == "bad limit" {
				continue // the daily endpoint ignores limit like the per-wallet one
			}
			if path == "/api/journal" && name == "bad tz" {
				continue
			}
			if code, body := multiGet(f, path, q); code != http.StatusBadRequest {
				t.Errorf("%s %s: status = %d %s", path, name, code, body)
			}
		}
		if rec := request(f.anon, http.MethodGet, path+"?wallet_ids="+itoa(f.charID), nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous status = %d", path, rec.Code)
		}
	}
	for name, cur := range map[string]string{"two parts": "5-1", "garbage": "a-b-c", "negative": "1-2--3"} {
		if code, _ := multiGet(f, "/api/journal", url.Values{"wallet_ids": {ids2(f)}, "cursor": {cur}}); code != http.StatusBadRequest {
			t.Errorf("cursor %s: status = %d", name, code)
		}
	}
	// The per-wallet cursor format is unchanged: three parts are rejected there.
	if got, code, _ := journalGet(f, f.charID, url.Values{"cursor": {"5-1-2"}}); code != http.StatusBadRequest {
		t.Errorf("per-wallet 3-part cursor = %d %+v", code, got)
	}
}

func TestJournalDailyAcrossWalletsSumsZeroFillsAndHonoursTimeZone(t *testing.T) {
	f := newFixture(t, nil, true)
	d := func(day, h int) time.Time { return time.Date(2026, 4, day, h, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	if _, err := f.st.AddJournalEntries(ctx, f.charID, []store.JournalEntry{
		{ID: 1, Date: d(27, 8), AmountCents: 100, RefType: "bounty_prizes"},
		{ID: 2, Date: d(27, 9), AmountCents: -30, RefType: "market_fee"},
		{ID: 3, Date: d(30, 23), AmountCents: -7, RefType: "market_fee"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddJournalEntries(ctx, f.corpID, []store.JournalEntry{
		{ID: 1, Date: d(27, 10), AmountCents: 50, RefType: "corp_tax"},
		{ID: 2, Date: d(29, 1), AmountCents: -5, RefType: "market_fee"},
	}); err != nil {
		t.Fatal(err)
	}
	ids := ids2(f)
	var got dailyResp
	code, body := multiGet(f, "/api/journal/daily", url.Values{"wallet_ids": {ids}, "tz": {"UTC"}})
	_ = json.Unmarshal([]byte(body), &got)
	want := [][3]any{
		{"2026-04-27", int64(150), int64(30)},
		{"2026-04-28", int64(0), int64(0)},
		{"2026-04-29", int64(0), int64(5)},
		{"2026-04-30", int64(0), int64(7)},
	}
	if code != http.StatusOK || !reflect.DeepEqual(got.rows(), want) {
		t.Fatalf("days = %d %s", code, body)
	}
	got = dailyResp{}
	_, body = multiGet(f, "/api/journal/daily", url.Values{"wallet_ids": {ids}, "tz": {"Asia/Tokyo"}})
	_ = json.Unmarshal([]byte(body), &got)
	if len(got.Days) == 0 || got.Days[len(got.Days)-1].Day != "2026-05-01" {
		t.Fatalf("tokyo days = %v", got.rows())
	}
	// ref_type narrows the sum to one type across both wallets.
	got = dailyResp{}
	_, body = multiGet(f, "/api/journal/daily", url.Values{"wallet_ids": {ids}, "ref_type": {"market_fee"}})
	_ = json.Unmarshal([]byte(body), &got)
	if !reflect.DeepEqual(got.rows(), [][3]any{
		{"2026-04-27", int64(0), int64(30)}, {"2026-04-28", int64(0), int64(0)},
		{"2026-04-29", int64(0), int64(5)}, {"2026-04-30", int64(0), int64(7)},
	}) {
		t.Fatalf("by type = %s", body)
	}
	// An empty window answers an empty list; without from it spans 30 days.
	got = dailyResp{}
	_, body = multiGet(f, "/api/journal/daily", url.Values{"wallet_ids": {ids}, "from": {d(1, 0).Format(time.RFC3339)}, "to": {d(2, 0).Format(time.RFC3339)}})
	_ = json.Unmarshal([]byte(body), &got)
	if len(got.Days) != 0 || !strings.Contains(body, `"days":[]`) {
		t.Fatalf("empty = %s", body)
	}
}
