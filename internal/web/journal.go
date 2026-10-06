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
	// defaultDailyDays is the chart window when neither from nor to is given.
	defaultDailyDays = 30
	maxTZLen         = 64
)

type journalEntryJSON struct {
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
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}
	wallets, err := s.deps.Store.WalletsForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	visible := false
	for _, wl := range wallets {
		if wl.ID == id {
			visible = true
			break
		}
	}
	if !visible {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	f, err := parseJournalQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.WalletID = id
	pageSize := f.Limit
	f.Limit = pageSize + 1 // one extra row tells whether another page exists
	rows, err := s.deps.Store.Journal(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	types, err := s.deps.Store.JournalRefTypes(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	var next *string
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[pageSize-1]
		c := strconv.FormatInt(last.Date.Unix(), 10) + "-" + strconv.FormatInt(last.ID, 10)
		next = &c
	}
	entries := make([]journalEntryJSON, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, journalEntryJSON{ID: e.ID, Date: e.Date.Unix(), Cents: e.AmountCents, RefType: e.RefType, Description: e.Description})
	}
	if types == nil {
		types = []string{}
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"entries": entries, "next_cursor": next, "ref_types": types})
}

type dailyTotalJSON struct {
	Day          string `json:"day"`
	IncomeCents  int64  `json:"income_cents"`
	ExpenseCents int64  `json:"expense_cents"`
}

// walletJournalDaily answers the income and expenses per local day of a wallet
// the user can see, for the ref_type, from and to filters of the journal and a
// tz IANA zone name (UTC when absent). Without from and to the window is the
// last 30 days. Days run ascending and are zero-filled between the first and
// the last one; expenses are positive magnitudes.
func (s *server) walletJournalDaily(w http.ResponseWriter, r *http.Request, u store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}
	wallets, err := s.deps.Store.WalletsForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	visible := false
	for _, wl := range wallets {
		if wl.ID == id {
			visible = true
			break
		}
	}
	if !visible {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}

	f, err := parseJournalQuery(r)
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
	f.WalletID = id
	if f.From.IsZero() && f.To.IsZero() {
		f.From = s.now().AddDate(0, 0, -defaultDailyDays)
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
// the page size on return.
func parseJournalQuery(r *http.Request) (store.JournalFilter, error) {
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
		date, id, ok := strings.Cut(raw, "-")
		sec, err1 := strconv.ParseInt(date, 10, 64)
		entry, err2 := strconv.ParseInt(id, 10, 64)
		if !ok || err1 != nil || err2 != nil || sec < 0 || entry < 0 {
			return f, errors.New("invalid cursor")
		}
		f.After = &store.JournalCursor{Date: time.Unix(sec, 0).UTC(), ID: entry}
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
