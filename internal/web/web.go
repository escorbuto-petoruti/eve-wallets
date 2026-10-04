// Package web serves the local wallet charts page and its read-only JSON API.
package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

const (
	maxWalletIDs = 50
	maxRange     = 10 * 365 * 24 * time.Hour
)

//go:embed static/index.html static/app.js static/style.css static/chart.umd.min.js
var assets embed.FS

// Deps are the collaborators of the handler.
type Deps struct {
	Store *store.Store
	// Status returns the last collection summary. It may be nil.
	Status func() StatusSnapshot
	// SSO signs users in. Without it /auth/login answers 503.
	SSO SSO
	// Now is the clock; it defaults to time.Now.
	Now func() time.Time
	// OnLogin runs after a successful sign-in with the user id. It may be nil.
	OnLogin func(userID int64)
	// OnTokenSaved runs right after a sign-in saved new credentials for a
	// character, before OnLogin. It may be nil.
	OnTokenSaved func(characterID int64)
	// AllowedPort is the port of the listener: a Host header may carry it (or no
	// port) next to a loopback name. Any other port is refused.
	AllowedPort string
}

type server struct {
	deps  Deps
	flows *loginFlows
	moves *pendingMoves
}

// tokenSaved tells the owner of the token cache that a sign-in stored new
// credentials for the character.
func (s *server) tokenSaved(characterID int64) {
	if s.deps.OnTokenSaved != nil {
		s.deps.OnTokenSaved(characterID)
	}
}

func (s *server) now() time.Time { return s.deps.Now() }

// New returns the handler for the local web app.
func New(deps Deps) http.Handler {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	s := &server{deps: deps}
	s.flows = newLoginFlows(s.now)
	s.moves = newPendingMoves(s.now)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", s.index)
	mux.HandleFunc("/static/{name}", s.static)
	mux.HandleFunc("GET /auth/login", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("GET /auth/add-character", s.addCharacter)
	mux.HandleFunc("GET /auth/confirm-move", s.confirmMove)
	mux.HandleFunc("POST /auth/move-character", s.moveCharacter)
	mux.HandleFunc("POST /auth/cancel-move", s.cancelMove)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("/api/me", requireUser(s.me))
	mux.HandleFunc("/api/wallets", requireUser(s.wallets))
	mux.HandleFunc("POST /api/wallets/{id}/label", requireUser(s.renameWallet))
	mux.HandleFunc("GET /api/wallets/{id}/journal", requireUser(s.walletJournal))
	mux.HandleFunc("/api/series", requireUser(s.series))
	mux.HandleFunc("/api/status", requireUser(s.status))
	return s.guard(s.withSession(mux))
}

// postPaths are the only fixed paths that accept a POST.
var postPaths = map[string]bool{
	"/auth/logout":         true,
	"/auth/move-character": true,
	"/auth/cancel-move":    true,
}

// walletLabelPath matches /api/wallets/{id}/label, the one POST route with a
// path parameter. The handler validates the id.
func walletLabelPath(p string) bool {
	rest, ok := strings.CutPrefix(p, "/api/wallets/")
	if !ok {
		return false
	}
	id, ok := strings.CutSuffix(rest, "/label")
	return ok && id != "" && !strings.Contains(id, "/")
}

// guard sets the hardening headers on every response, refuses a Host that is not
// the local app (DNS rebinding) and allows only GET and HEAD, except for the
// POSTs of /auth/logout, /auth/move-character, /auth/cancel-move and
// /api/wallets/{id}/label.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://images.evetech.net")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !allowedHost(r.Host, s.deps.AllowedPort) {
			writeError(w, http.StatusForbidden, "forbidden host")
			return
		}
		post := r.Method == http.MethodPost && (postPaths[r.URL.Path] || walletLabelPath(r.URL.Path))
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !post {
			h.Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	serveAsset(w, r, "static/index.html", "text/html; charset=utf-8")
}

var staticTypes = map[string]string{
	"app.js":           "text/javascript; charset=utf-8",
	"style.css":        "text/css; charset=utf-8",
	"chart.umd.min.js": "text/javascript; charset=utf-8",
	"index.html":       "text/html; charset=utf-8",
}

func (s *server) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctype, ok := staticTypes[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveAsset(w, r, "static/"+name, ctype)
}

func serveAsset(w http.ResponseWriter, r *http.Request, name, ctype string) {
	b, err := fs.ReadFile(assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	// Embedded assets carry no validators, so ask the browser to refetch them
	// instead of reusing a heuristically cached copy of an older build.
	w.Header().Set("Cache-Control", "no-cache")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(b)
}

type walletJSON struct {
	ID          int64            `json:"id"`
	Kind        store.Kind       `json:"kind"`
	OwnerID     int64            `json:"owner_id"`
	OwnerName   string           `json:"owner_name"`
	Division    int              `json:"division"`
	Name        string           `json:"name"`
	NameSource  store.NameSource `json:"name_source"`
	Cents       *int64           `json:"cents"`
	BalanceTime *int64           `json:"balance_time"`
}

func (s *server) wallets(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	// The wallet links are keyed by the user id, not the character id.
	wallets, err := s.deps.Store.WalletsForUser(ctx, u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	latest, err := s.deps.Store.LatestBalancesForUser(ctx, u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	byID := make(map[int64]store.WalletBalance, len(latest))
	for _, b := range latest {
		byID[b.Wallet.ID] = b
	}
	out := make([]walletJSON, 0, len(wallets))
	for _, wl := range wallets {
		item := walletJSON{ID: wl.ID, Kind: wl.Kind, OwnerID: wl.OwnerID, OwnerName: wl.OwnerName, Division: wl.Division,
			Name: wl.DisplayName(), NameSource: wl.NameSource()}
		if b, ok := byID[wl.ID]; ok {
			cents, at := b.Cents, b.At.Unix()
			item.Cents, item.BalanceTime = &cents, &at
		}
		out = append(out, item)
	}
	writeJSON(w, r, http.StatusOK, map[string]any{"wallets": out})
}

type seriesJSON struct {
	WalletID int64   `json:"wallet_id"`
	Points   []Point `json:"points"`
}

func (s *server) series(w http.ResponseWriter, r *http.Request, u store.User) {
	q := r.URL.Query()
	ids, err := parseIDs(q.Get("wallet_ids"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := parseTime(q.Get("from"), "from")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := parseTime(q.Get("to"), "to")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !from.IsZero() && !to.IsZero() {
		if to.Before(from) {
			writeError(w, http.StatusBadRequest, "to must not be before from")
			return
		}
		if to.Sub(from) > maxRange {
			writeError(w, http.StatusBadRequest, "time range too large")
			return
		}
	}
	withTotal := false
	switch q.Get("total") {
	case "", "0", "false":
	case "1", "true":
		withTotal = true
	default:
		writeError(w, http.StatusBadRequest, "total must be 0 or 1")
		return
	}

	out := map[string]any{"series": []seriesJSON{}}
	if len(ids) > 0 || !q.Has("wallet_ids") {
		// The wallet links are keyed by the user id, not the character id.
		pts, err := s.deps.Store.SeriesForUser(r.Context(), u.UserID, store.SeriesFilter{WalletIDs: ids, From: from, To: to})
		if err != nil {
			serverError(w, err)
			return
		}
		grouped := groupPoints(ids, pts)
		list := make([]seriesJSON, 0, len(grouped))
		byWallet := make(map[int64][]Point, len(grouped))
		for _, g := range grouped {
			list = append(list, g)
			byWallet[g.WalletID] = g.Points
		}
		out["series"] = list
		if withTotal {
			out["total"] = SumForwardFill(byWallet)
		}
	}
	writeJSON(w, r, http.StatusOK, out)
}

// groupPoints groups ordered store points by wallet. Requested ids with no
// points still get an (empty) entry, in request order; when no ids were
// requested, wallets appear in store order.
func groupPoints(ids []int64, pts []store.Point) []seriesJSON {
	index := make(map[int64]int)
	var out []seriesJSON
	add := func(id int64) int {
		if i, ok := index[id]; ok {
			return i
		}
		out = append(out, seriesJSON{WalletID: id, Points: []Point{}})
		index[id] = len(out) - 1
		return len(out) - 1
	}
	for _, id := range ids {
		add(id)
	}
	for _, p := range pts {
		i := add(p.WalletID)
		out[i].Points = append(out[i].Points, Point{T: p.At.Unix(), Cents: p.Cents})
	}
	return out
}

func parseIDs(raw string) ([]int64, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxWalletIDs {
		return nil, errors.New("too many wallet ids (maximum " + strconv.Itoa(maxWalletIDs) + ")")
	}
	seen := make(map[int64]bool, len(parts))
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("wallet_ids must be positive integers separated by commas")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func parseTime(raw, name string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New(name + " must be an RFC 3339 time")
	}
	return t, nil
}

// status scopes the last collection to the signed-in user. A skip or an
// attributed error is shown only when the user can see its owner: their own
// characters, or a corporation of their linked wallets. An error without an
// owner identity is a run-level error (the collector process, not a user) and
// is shown to every signed-in user; a skip without one is never shown (a skip
// always belongs to someone, a missing identity is a bug). The counters stay
// global: they describe the process cycle.
func (s *server) status(w http.ResponseWriter, r *http.Request, u store.User) {
	var st StatusSnapshot
	if s.deps.Status != nil {
		st = s.deps.Status()
	}
	// The wallet links are keyed by the user id (sessions.user_id), not the
	// character id: scope by u.UserID. A character skip belongs to the
	// signed-in character, and its OwnerID is that character's id, so it stays
	// compared to u.CharacterID.
	wallets, err := s.deps.Store.WalletsForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	corps := make(map[int64]bool, len(wallets))
	for _, wl := range wallets {
		if wl.Kind == store.KindCorporation {
			corps[wl.OwnerID] = true
		}
	}
	chars, err := s.deps.Store.CharactersForUser(r.Context(), u.UserID)
	if err != nil {
		serverError(w, err)
		return
	}
	own := make(map[int64]bool, len(chars))
	for _, c := range chars {
		own[c.CharacterID] = true
	}
	canSee := func(kind store.Kind, ownerID int64) bool {
		switch kind {
		case store.KindCharacter:
			return own[ownerID] // a character skip carries a character id
		case store.KindCorporation:
			return corps[ownerID]
		default:
			return false
		}
	}
	skipped := []SkippedItem{}
	for _, it := range st.Skipped {
		// Only the skips this user's own characters produced: another user's
		// character can hit a 403 on a corporation this user also sees.
		if canSee(it.OwnerKind, it.OwnerID) && it.UserID == u.UserID {
			skipped = append(skipped, it)
		}
	}
	errs := []string{}
	reauth := []ReauthItem{}
	seenReauth := map[int64]bool{}
	for _, it := range st.Errors {
		if it.OwnerKind == "" || canSee(it.OwnerKind, it.OwnerID) {
			errs = append(errs, it.Message)
		}
		// A reauth entry names a character, so it is listed only for the user
		// who owns that character, whatever the error's own scope (a run-level
		// error must not leak another user's character).
		if it.Reauth != nil && own[it.Reauth.CharacterID] && !seenReauth[it.Reauth.CharacterID] {
			seenReauth[it.Reauth.CharacterID] = true
			reauth = append(reauth, *it.Reauth)
		}
	}
	var takenAt *int64
	if st.TakenAt != 0 {
		takenAt = &st.TakenAt
	}
	writeJSON(w, r, http.StatusOK, map[string]any{
		"taken_at":            takenAt,
		"snapshots":           st.Snapshots,
		"journal_points":      st.JournalPoints,
		"skipped":             skipped,
		"errors":              errs,
		"reauth":              reauth,
		"rate_limited":        st.RateLimited,
		"retry_after_seconds": st.RetryAfterSeconds,
	})
}

func writeJSON(w http.ResponseWriter, r *http.Request, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // escapes <, > and & by default
	if err := enc.Encode(v); err != nil {
		code, buf = http.StatusInternalServerError, *bytes.NewBufferString(`{"error":"internal error"}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(code)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, nil, code, map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
