package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// Journal page sizes and the longest accepted ref_type filter.
const (
	defaultJournalLimit = 50
	maxJournalLimit     = 200
	maxRefTypeLen       = 64
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
