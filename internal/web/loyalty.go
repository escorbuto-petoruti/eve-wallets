package web

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// scopeLoyalty is the ESI scope a character must grant to show loyalty points.
const scopeLoyalty = "esi-characters.read_loyalty.v1"

type loyaltyCorporationJSON struct {
	CorporationID int64  `json:"corporation_id"`
	Name          string `json:"name"`
	Points        int64  `json:"points"`
}

type loyaltyCharacterJSON struct {
	CharacterID   int64                    `json:"character_id"`
	CharacterName string                   `json:"character_name"`
	FetchedAt     *int64                   `json:"fetched_at"`
	NeedsReauth   bool                     `json:"needs_reauth"`
	Corporations  []loyaltyCorporationJSON `json:"corporations"`
}

// loyalty lists the stored loyalty points of the signed-in user's own
// characters, per issuing corporation and by points descending. A character
// whose token lacks the loyalty scope has needs_reauth set and no corporations:
// it must sign in again. Corporations without a cached name show "Corp <id>".
func (s *server) loyalty(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	chars, err := s.deps.Store.CharactersForUser(ctx, u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	scopes, err := s.deps.Store.ScopesForUser(ctx, u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	rows, err := s.deps.Store.LoyaltyForUser(ctx, u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CorporationID)
	}
	names, err := s.deps.Store.CorporationNames(ctx, ids)
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]loyaltyCharacterJSON, 0, len(chars))
	byChar := make(map[int64]int, len(chars))
	for _, c := range chars {
		byChar[c.CharacterID] = len(out)
		out = append(out, loyaltyCharacterJSON{
			CharacterID: c.CharacterID, CharacterName: c.Name,
			NeedsReauth:  !slices.Contains(scopes[c.CharacterID], scopeLoyalty),
			Corporations: []loyaltyCorporationJSON{},
		})
	}
	for _, row := range rows { // by character, then points descending
		i, ok := byChar[row.CharacterID]
		if !ok || out[i].NeedsReauth {
			continue
		}
		name := names[row.CorporationID]
		if name == "" {
			name = fmt.Sprintf("Corp %d", row.CorporationID)
		}
		at := row.FetchedAt.Unix()
		out[i].FetchedAt = &at
		out[i].Corporations = append(out[i].Corporations, loyaltyCorporationJSON{CorporationID: row.CorporationID, Name: name, Points: row.Points})
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"characters": out})
}

const (
	defaultLoyaltyHistoryLimit = 500
	maxLoyaltyHistoryLimit     = 2000
)

type loyaltyHistoryPointJSON struct {
	CorporationID int64  `json:"corporation_id"`
	Name          string `json:"name"`
	TakenAt       int64  `json:"taken_at"` // unix seconds
	Points        int64  `json:"points"`
}

// loyaltyHistory lists the recorded points of one of the user's own
// characters, by time and then corporation ascending. It pages with an opaque
// keyset cursor "<taken_at>-<corporation_id>" of the last row of the previous
// page; next_cursor is null on the last page. An unknown character and one of
// another user both answer 404.
func (s *server) loyaltyHistory(w http.ResponseWriter, r *http.Request, u store.User) {
	q := r.URL.Query()
	charID, err := strconv.ParseInt(q.Get("character_id"), 10, 64)
	if err != nil || charID <= 0 {
		writeError(w, http.StatusBadRequest, "character_id is required")
		return
	}
	chars, err := s.deps.Store.CharactersForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	if !slices.ContainsFunc(chars, func(c store.UserCharacter) bool { return c.CharacterID == charID }) {
		writeError(w, http.StatusNotFound, "character not found")
		return
	}
	f, pageSize, err := parseLoyaltyHistoryQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	f.CharacterID = charID
	f.Limit = pageSize + 1 // one extra row tells whether another page exists
	rows, err := s.deps.Store.LoyaltyHistory(r.Context(), f)
	if err != nil {
		serverError(w, err)
		return
	}
	var next *string
	if len(rows) > pageSize {
		rows = rows[:pageSize]
		last := rows[pageSize-1]
		c := strconv.FormatInt(last.TakenAt.Unix(), 10) + "-" + strconv.FormatInt(last.CorporationID, 10)
		next = &c
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.CorporationID)
	}
	names, err := s.deps.Store.CorporationNames(r.Context(), ids)
	if err != nil {
		serverError(w, err)
		return
	}
	points := make([]loyaltyHistoryPointJSON, 0, len(rows))
	for _, row := range rows {
		name := names[row.CorporationID]
		if name == "" {
			name = fmt.Sprintf("Corp %d", row.CorporationID)
		}
		points = append(points, loyaltyHistoryPointJSON{CorporationID: row.CorporationID, Name: name, TakenAt: row.TakenAt.Unix(), Points: row.Points})
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"character_id": charID, "points": points, "next_cursor": next})
}

// parseLoyaltyHistoryQuery reads corporation_id, from, to, limit and cursor,
// returning the page size separately.
func parseLoyaltyHistoryQuery(r *http.Request) (store.LoyaltyHistoryFilter, int, error) {
	q := r.URL.Query()
	var f store.LoyaltyHistoryFilter
	pageSize := defaultLoyaltyHistoryLimit
	if raw := q.Get("corporation_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return f, 0, errors.New("corporation_id must be a positive integer")
		}
		f.CorporationID = id
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLoyaltyHistoryLimit {
			return f, 0, errors.New("limit must be between 1 and " + strconv.Itoa(maxLoyaltyHistoryLimit))
		}
		pageSize = n
	}
	if raw := q.Get("cursor"); raw != "" {
		at, corp, ok := strings.Cut(raw, "-")
		sec, err1 := strconv.ParseInt(at, 10, 64)
		id, err2 := strconv.ParseInt(corp, 10, 64)
		if !ok || err1 != nil || err2 != nil || sec < 0 || id < 0 {
			return f, 0, errors.New("invalid cursor")
		}
		f.After = &store.LoyaltyHistoryCursor{TakenAt: time.Unix(sec, 0).UTC(), CorporationID: id}
	}
	var err error
	if f.From, err = parseTime(q.Get("from"), "from"); err != nil {
		return f, 0, err
	}
	if f.To, err = parseTime(q.Get("to"), "to"); err != nil {
		return f, 0, err
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.To.Before(f.From) {
		return f, 0, errors.New("to must not be before from")
	}
	return f, pageSize, nil
}
