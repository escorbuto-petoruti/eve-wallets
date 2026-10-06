package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names must resolve on hosts without a zoneinfo database (Windows)

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// Journal page sizes and the longest accepted ref_type filter.
const (
	defaultJournalLimit = 50
	maxJournalLimit     = 200
	maxRefTypeLen       = 64
	// defaultDailyDays is the chart window when from is absent.
	defaultDailyDays = 30
	maxTZLen         = 64
)

type journalEntryJSON struct {
	// WalletID is set on the entries of the multi-wallet journal only.
	WalletID    int64  `json:"wallet_id,omitempty"`
	ID          int64  `json:"id"`
	Date        int64  `json:"date"` // unix seconds
	Cents       int64  `json:"cents"`
	RefType     string `json:"ref_type"`
	Description string `json:"description"`
}

// walletJournal pages through the stored journal of a wallet the user can see,
// newest first. The cursor is the keyset position "<date>-<id>" of the last
// entry of the previous page, so entries arriving later never shift a page.
// An unknown wallet and one of another user both answer 404.
func (s *server) walletJournal(w http.ResponseWriter, r *http.Request, u store.User) {
	id, ok := s.pathWallet(w, r, u)
	if !ok {
		return
	}
	s.journalPage(w, r, []int64{id}, false)
}

// journals pages through the stored journals of the wallet_ids the user can
// see, merged newest first. Entry ids repeat across wallets, so entries carry
// their wallet_id and the cursor is "<date>-<id>-<wallet_id>".
func (s *server) journals(w http.ResponseWriter, r *http.Request, u store.User) {
	ids, ok := s.queryWallets(w, r, u)
	if !ok {
		return
	}
	s.journalPage(w, r, ids, true)
}

// journalPage writes one page of the journal of ids (one wallet unless multi).
func (s *server) journalPage(w http.ResponseWriter, r *http.Request, ids []int64, multi bool) {
	f, err := parseJournalQuery(r, multi)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if multi {
		f.WalletIDs = ids
	} else {
		f.WalletID = ids[0]
	}
	pageSize := f.Limit
	f.Limit = pageSize + 1 // one extra row tells whether another page exists
	rows, err := s.deps.Store.Journal(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	var types []string
	if multi {
		types, err = s.deps.Store.JournalRefTypesFor(r.Context(), ids)
	} else {
		types, err = s.deps.Store.JournalRefTypes(r.Context(), ids[0])
	}
	if err != nil {
		serverError(w, err)
		return
	}
	var next *string
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[pageSize-1]
		c := strconv.FormatInt(last.Date.Unix(), 10) + "-" + strconv.FormatInt(last.ID, 10)
		if multi {
			c += "-" + strconv.FormatInt(last.WalletID, 10)
		}
		next = &c
	}
	entries := make([]journalEntryJSON, 0, len(rows))
	for _, e := range rows {
		je := journalEntryJSON{ID: e.ID, Date: e.Date.Unix(), Cents: e.AmountCents, RefType: e.RefType, Description: e.Description}
		if multi {
			je.WalletID = e.WalletID
		}
		entries = append(entries, je)
	}
	if types == nil {
		types = []string{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"entries": entries, "next_cursor": next, "ref_types": types})
}

// pathWallet resolves the {id} path value to a wallet the user can see; an
// unknown wallet and one of another user both answer 404.
func (s *server) pathWallet(w http.ResponseWriter, r *http.Request, u store.User) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "wallet not found")
		return 0, false
	}
	if !s.allVisible(w, r, u, []int64{id}) {
		return 0, false
	}
	return id, true
}

// queryWallets parses the wallet_ids parameter (required, same cap as
// /api/series) and answers 404 unless every id is visible to the user.
func (s *server) queryWallets(w http.ResponseWriter, r *http.Request, u store.User) ([]int64, bool) {
	ids, err := parseIDs(r.URL.Query().Get("wallet_ids"))
	if err == nil && len(ids) == 0 {
		err = errors.New("wallet_ids is required")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	if !s.allVisible(w, r, u, ids) {
		return nil, false
	}
	return ids, true
}

// allVisible reports whether every id is a wallet the user can see; otherwise
// it answers 404 (or 500 when the lookup fails) and returns false.
func (s *server) allVisible(w http.ResponseWriter, r *http.Request, u store.User, ids []int64) bool {
	wallets, err := s.deps.Store.WalletsForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return false
	}
	visible := make(map[int64]bool, len(wallets))
	for _, wl := range wallets {
		visible[wl.ID] = true
	}
	for _, id := range ids {
		if !visible[id] {
			writeError(w, http.StatusNotFound, "wallet not found")
			return false
		}
	}
	return true
}

type dailyTotalJSON struct {
	Day          string `json:"day"`
	IncomeCents  int64  `json:"income_cents"`
	ExpenseCents int64  `json:"expense_cents"`
}

// walletJournalDaily answers the income and expenses per local day of a wallet
// the user can see, for the ref_type, from and to filters of the journal and a
// tz IANA zone name (UTC when absent). Without from the window starts 30 days
// before to (or before now when to is absent). Days run ascending and are zero-filled between the first and
// the last one; expenses are positive magnitudes.
func (s *server) walletJournalDaily(w http.ResponseWriter, r *http.Request, u store.User) {
	id, ok := s.pathWallet(w, r, u)
	if !ok {
		return
	}
	s.dailyTotalsFor(w, r, store.JournalFilter{WalletID: id})
}

// journalsDaily is walletJournalDaily summed across the wallet_ids the user can
// see.
func (s *server) journalsDaily(w http.ResponseWriter, r *http.Request, u store.User) {
	ids, ok := s.queryWallets(w, r, u)
	if !ok {
		return
	}
	s.dailyTotalsFor(w, r, store.JournalFilter{WalletIDs: ids})
}

// dailyTotalsFor answers the daily totals of the wallets selected by base.
func (s *server) dailyTotalsFor(w http.ResponseWriter, r *http.Request, base store.JournalFilter) {
	f, err := parseJournalQuery(r, len(base.WalletIDs) > 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	loc := time.UTC
	if name := r.URL.Query().Get("tz"); name != "" {
		if len(name) > maxTZLen {
			writeError(w, http.StatusBadRequest, "tz is too long")
			return
		}
		if loc, err = time.LoadLocation(name); err != nil {
			writeError(w, http.StatusBadRequest, "tz must be an IANA time zone name")
			return
		}
	}
	f.WalletID, f.WalletIDs = base.WalletID, base.WalletIDs
	if f.From.IsZero() {
		end := f.To
		if end.IsZero() {
			end = s.now()
		}
		f.From = end.AddDate(0, 0, -defaultDailyDays)
	}
	rows, err := s.deps.Store.JournalAmounts(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"days": dailyTotals(rows, loc)})
}

// dailyTotals sums rows (oldest first) per calendar day of loc, filling the
// days without rows between the first and the last one with zeros.
func dailyTotals(rows []store.JournalAmount, loc *time.Location) []dailyTotalJSON {
	out := []dailyTotalJSON{}
	if len(rows) == 0 {
		return out
	}
	const layout = "2006-01-02"
	byDay := map[string]*dailyTotalJSON{}
	for _, a := range rows {
		day := a.Date.In(loc).Format(layout)
		t := byDay[day]
		if t == nil {
			t = &dailyTotalJSON{Day: day}
			byDay[day] = t
		}
		if a.AmountCents >= 0 {
			t.IncomeCents += a.AmountCents
		} else {
			t.ExpenseCents -= a.AmountCents
		}
	}
	// Walk calendar dates as UTC midnights so a 23 or 25 hour local day
	// still advances by exactly one date.
	first, _ := time.Parse(layout, rows[0].Date.In(loc).Format(layout))
	last, _ := time.Parse(layout, rows[len(rows)-1].Date.In(loc).Format(layout))
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		day := d.Format(layout)
		if t := byDay[day]; t != nil {
			out = append(out, *t)
		} else {
			out = append(out, dailyTotalJSON{Day: day})
		}
	}
	return out
}

// parseJournalQuery reads limit, cursor, ref_type, from and to. Limit holds
// the page size on return. The cursor is "<date>-<id>", or "<date>-<id>-<wallet_id>"
// when multi is set.
func parseJournalQuery(r *http.Request, multi bool) (store.JournalFilter, error) {
	q := r.URL.Query()
	f := store.JournalFilter{Limit: defaultJournalLimit}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxJournalLimit {
			return f, errors.New("limit must be between 1 and " + strconv.Itoa(maxJournalLimit))
		}
		f.Limit = n
	}
	if raw := q.Get("cursor"); raw != "" {
		parts := strings.Split(raw, "-")
		want := 2
		if multi {
			want = 3
		}
		if len(parts) != want {
			return f, errors.New("invalid cursor")
		}
		nums := make([]int64, want)
		for i, p := range parts {
			n, err := strconv.ParseInt(p, 10, 64)
			if err != nil || n < 0 {
				return f, errors.New("invalid cursor")
			}
			nums[i] = n
		}
		f.After = &store.JournalCursor{Date: time.Unix(nums[0], 0).UTC(), ID: nums[1]}
		if multi {
			f.After.WalletID = nums[2]
		}
	}
	f.RefType = q.Get("ref_type")
	if len(f.RefType) > maxRefTypeLen {
		return f, errors.New("ref_type is too long")
	}
	var err error
	if f.From, err = parseTime(q.Get("from"), "from"); err != nil {
		return f, err
	}
	if f.To, err = parseTime(q.Get("to"), "to"); err != nil {
		return f, err
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return f, errors.New("to must not be before from")
	}
	return f, nil
}
