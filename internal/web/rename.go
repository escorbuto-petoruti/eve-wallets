package web

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// maxRenameBody bounds the JSON body of a rename: a 64-character name needs
// well under this, even with escapes.
const maxRenameBody = 1 << 10

type renamedJSON struct {
	ID         int64            `json:"id"`
	Name       string           `json:"name"`
	NameSource store.NameSource `json:"name_source"`
}

// renameWallet sets, or with an empty name clears, the label of a corporation
// wallet the user can see. It is the only API write: it needs a session, the
// sign-out same-origin check and a JSON body. Personal wallets, the corporation
// master wallet (division 1) and wallets named by ESI cannot be renamed here.
// An unknown wallet and one of another user both answer 404.
func (s *server) renameWallet(w http.ResponseWriter, r *http.Request, u store.User) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-site request refused")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return
	}
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
	var wl *store.Wallet
	for i := range wallets {
		if wallets[i].ID == id {
			wl = &wallets[i]
			break
		}
	}
	if wl == nil {
		writeError(w, http.StatusNotFound, "wallet not found")
		return
	}
	switch {
	case wl.Kind != store.KindCorporation:
		writeError(w, http.StatusForbidden, "only corporation wallets can be renamed")
		return
	case wl.Division == 1:
		writeError(w, http.StatusForbidden, "the corporation Master Wallet cannot be renamed")
		return
	case wl.NameSource() == store.NameESI:
		writeError(w, http.StatusForbidden, "this wallet is named by ESI and cannot be renamed")
		return
	}

	var body struct {
		Name *string `json:"name"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRenameBody))
	err = dec.Decode(&body)
	if err == nil && dec.More() {
		err = errors.New("trailing data")
	}
	if err != nil || body.Name == nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, `body must be {"name": "..."}`)
		return
	}

	name := strings.TrimSpace(*body.Name)
	if name == "" {
		err = s.deps.Store.ClearLabel(r.Context(), id)
		wl.Label = ""
	} else {
		err = s.deps.Store.SetLabel(r.Context(), id, name)
		wl.Label = name
	}
	switch {
	case errors.Is(err, store.ErrInvalidName):
		writeError(w, http.StatusBadRequest, "name must be 1-64 characters without control characters")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "wallet not found")
	case err != nil:
		serverError(w, err)
	default:
		writeJSON(w, r, http.StatusOK, renamedJSON{ID: id, Name: wl.DisplayName(), NameSource: wl.NameSource()})
	}
}
