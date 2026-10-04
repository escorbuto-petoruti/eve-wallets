package web

import (
	"fmt"
	"net/http"
	"slices"

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
