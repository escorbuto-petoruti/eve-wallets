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
}

type server struct {
	deps Deps
}

// New returns the handler for the local web app.
func New(deps Deps) http.Handler {
	s := &server{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", s.index)
	mux.HandleFunc("/static/{name}", s.static)
	mux.HandleFunc("/api/wallets", s.wallets)
	mux.HandleFunc("/api/series", s.series)
	mux.HandleFunc("/api/status", s.status)
	return guard(mux)
}

// guard sets the hardening headers on every response and allows only GET and
// HEAD.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
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

func (s *server) wallets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wallets, err := s.deps.Store.Wallets(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	latest, err := s.deps.Store.LatestBalances(ctx)
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

func (s *server) series(w http.ResponseWriter, r *http.Request) {
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
		pts, err := s.deps.Store.Series(r.Context(), store.SeriesFilter{WalletIDs: ids, From: from, To: to})
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

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	var st StatusSnapshot
	if s.deps.Status != nil {
		st = s.deps.Status()
	}
	skipped := st.Skipped
	if skipped == nil {
		skipped = []SkippedItem{}
	}
	errs := st.Errors
	if errs == nil {
		errs = []string{}
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
