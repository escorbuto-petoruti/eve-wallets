// Package collector takes one snapshot of every wallet of every character.
//
// A failure for one character or wallet never aborts the run: it is recorded
// in the Report and collection continues. ESI rate limiting is the exception:
// the run stops issuing calls and returns the partial report.
package collector

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// ESI scopes and skip reasons.
const (
	ScopeCharacterWallet   = "esi-wallet.read_character_wallet.v1"
	ScopeCorporationWallet = "esi-wallet.read_corporation_wallets.v1"
	ScopeCorporationNames  = "esi-corporations.read_divisions.v1"

	ReasonMissingRole       = "missing corporation role"
	ReasonMissingDirector   = "missing Director role"
	ReasonAlreadyCollected  = "already collected"
	reasonMissingScopeFmt   = "missing scope %s"
	corporationFallbackName = "Corp %d"
)

// ESIClient is the subset of *esi.Client the collector needs.
type ESIClient interface {
	CharacterWallet(ctx context.Context, token string, characterID int64) (int64, error)
	CharacterCorporationID(ctx context.Context, characterID int64) (int64, error)
	CorporationName(ctx context.Context, corporationID int64) (string, error)
	CorporationWallets(ctx context.Context, token string, corporationID int64) ([]esi.DivisionBalance, error)
	CorporationDivisions(ctx context.Context, token string, corporationID int64) (esi.DivisionNames, error)
	CharacterJournal(ctx context.Context, token string, characterID int64) ([]esi.JournalEntry, error)
	CorporationJournal(ctx context.Context, token string, corporationID int64, division int) ([]esi.JournalEntry, error)
}

// StoreWriter is the subset of *store.Store the collector needs.
type StoreWriter interface {
	UpsertWallet(ctx context.Context, w store.Wallet) (int64, error)
	AddSnapshot(ctx context.Context, walletID int64, takenAt time.Time, cents int64) error
	AddJournalBalance(ctx context.Context, walletID, entryID int64, at time.Time, cents int64) error
	SetESIName(ctx context.Context, walletID int64, name string) error
	ClearESIName(ctx context.Context, walletID int64) error
	// LinkWallet lets a user see a wallet; linking twice is a no-op.
	LinkWallet(ctx context.Context, userID, walletID int64) error
}

var _ ESIClient = (*esi.Client)(nil)

// Deps are the collaborators of a Collector. Now defaults to time.Now.
type Deps struct {
	Auth  auth.TokenSource
	ESI   ESIClient
	Store StoreWriter
	Now   func() time.Time
}

// Snapshot is one balance recorded during a run.
type Snapshot struct {
	Kind      store.Kind
	OwnerID   int64
	OwnerName string
	Division  int
	Cents     int64
}

// Skip records something that was deliberately not collected.
type Skip struct {
	Owner  string
	Reason string
}

// ItemError records a failure for one owner. It never contains a token.
type ItemError struct {
	Owner string
	Err   error
}

func (e ItemError) Error() string { return fmt.Sprintf("%s: %v", e.Owner, e.Err) }

// Report is the outcome of one run.
type Report struct {
	TakenAt   time.Time
	Snapshots []Snapshot
	Skipped   []Skip
	Errors    []ItemError
	// RateLimited is true when ESI rate limiting cut the run short; the report
	// then holds only what was collected before that. RetryAfter is ESI's hint.
	RateLimited bool
	RetryAfter  time.Duration
	// NamesUpdated counts the wallets whose ESI name was set or cleared.
	NamesUpdated int
	// JournalPoints counts the journal balances a backfill saw in the same
	// cycle. Run leaves it 0; the serve cycle fills it in.
	JournalPoints int
}

// Collector runs snapshot collections.
type Collector struct {
	deps Deps
}

// New returns a Collector.
func New(deps Deps) *Collector {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Collector{deps: deps}
}

// walker holds the state of one pass over every character and corporation.
// It owns the discovery rules shared by Run and Backfill; the two differ only
// in the callbacks that handle a personal wallet and a corporation.
type walker struct {
	c         *Collector
	collected map[int64]bool // corporations already handled
	cur       auth.Character // the character being walked
	// corpWallets are the stored wallet ids per corporation, so a later
	// character of another user can be linked without a second ESI fetch.
	corpWallets map[int64][]int64
	linked      map[[2]int64]bool // {user id, wallet id} pairs already linked
	named       map[int64]bool    // corporations whose division names were fetched
	skipped     []Skip
	errors      []ItemError
	limited     bool
	retry       time.Duration

	// personal handles the personal wallet of ch.
	personal func(ctx context.Context, ch auth.Character, token string) error
	// corporation handles one corporation the character can read, with the
	// divisions ESI returned for it.
	corporation func(ctx context.Context, corpID int64, name, token string, divisions []esi.DivisionBalance) error
	// names, when set, refreshes the division names of a corporation whose
	// wallets were collected. It is nil for passes that do not need them.
	names func(ctx context.Context, corpID int64, name, token string, names esi.DivisionNames) error
}

func newWalker(c *Collector) *walker {
	return &walker{
		c:           c,
		collected:   make(map[int64]bool),
		named:       make(map[int64]bool),
		corpWallets: make(map[int64][]int64),
		linked:      make(map[[2]int64]bool),
	}
}

// errStop is an internal signal that the run must end now.
var errStop = errors.New("collector: stop")

// walk visits every character. It returns nil when everything was visited or
// a rate limit stopped the pass (see limited), and the context error when the
// context ends.
func (w *walker) walk(ctx context.Context) error {
	chars, err := w.c.deps.Auth.Characters(ctx)
	if err != nil {
		return fmt.Errorf("collector: list characters: %w", err)
	}
	for _, ch := range chars {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.character(ctx, ch); err != nil {
			if errors.Is(err, errStop) {
				return nil
			}
			return err
		}
	}
	return ctx.Err()
}

// Run collects one snapshot of every wallet of every character. It returns an
// error only when the run cannot start (the character list is unavailable) or
// the context ends; per-item failures are in the Report.
func (c *Collector) Run(ctx context.Context) (Report, error) {
	w := newWalker(c)
	rep := Report{TakenAt: c.deps.Now().Truncate(time.Second)}
	w.personal = func(ctx context.Context, ch auth.Character, token string) error {
		cents, err := c.deps.ESI.CharacterWallet(ctx, token, ch.ID)
		if err != nil {
			return w.esiFailure(ctx, ch.Name, err)
		}
		w.record(ctx, &rep, store.Wallet{Kind: store.KindCharacter, OwnerID: ch.ID, OwnerName: ch.Name}, cents)
		return nil
	}
	// Wallet ids by corporation and division, filled as wallets are stored.
	walletIDs := make(map[int64]map[int]int64)
	w.corporation = func(ctx context.Context, corpID int64, name, _ string, divisions []esi.DivisionBalance) error {
		for _, d := range divisions {
			id, ok := w.record(ctx, &rep, store.Wallet{Kind: store.KindCorporation, OwnerID: corpID, OwnerName: name, Division: d.Division}, d.Cents)
			if !ok {
				continue
			}
			if walletIDs[corpID] == nil {
				walletIDs[corpID] = make(map[int]int64)
			}
			walletIDs[corpID][d.Division] = id
		}
		return nil
	}
	w.names = func(ctx context.Context, corpID int64, name, _ string, names esi.DivisionNames) error {
		for division, id := range walletIDs[corpID] {
			var err error
			if n, ok := names[division]; ok {
				err = c.deps.Store.SetESIName(ctx, id, n)
			} else {
				err = c.deps.Store.ClearESIName(ctx, id) // the default name is in use
			}
			if err != nil {
				w.fail(name, err)
				continue
			}
			rep.NamesUpdated++
		}
		return nil
	}
	err := w.walk(ctx)
	rep.Skipped, rep.Errors = w.skipped, w.errors
	rep.RateLimited, rep.RetryAfter = w.limited, w.retry
	if err != nil && ctx.Err() == nil {
		return Report{}, err // the character list is unavailable
	}
	return rep, err
}

// character handles the wallets of one character. It returns errStop after a
// rate limit and a context error when the context ends.
func (w *walker) character(ctx context.Context, ch auth.Character) error {
	w.cur = ch
	var token string
	tokenFailed := false
	getToken := func() (string, bool) {
		if token != "" {
			return token, true
		}
		if tokenFailed {
			return "", false
		}
		t, err := w.c.deps.Auth.Token(ctx, ch.ID)
		if err != nil {
			tokenFailed = true
			w.fail(ch.Name, err)
			return "", false
		}
		token = t
		return t, true
	}

	if !slices.Contains(ch.Scopes, ScopeCharacterWallet) {
		w.skip(ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCharacterWallet))
	} else if tok, ok := getToken(); ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.personal(ctx, ch, tok); err != nil {
			return err
		}
	}

	if !slices.Contains(ch.Scopes, ScopeCorporationWallet) {
		w.skip(ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCorporationWallet))
		return nil
	}
	return w.corp(ctx, ch, getToken)
}

func (w *walker) corp(ctx context.Context, ch auth.Character, getToken func() (string, bool)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	corpID, err := w.c.deps.ESI.CharacterCorporationID(ctx, ch.ID)
	if err != nil {
		return w.esiFailure(ctx, ch.Name, err)
	}
	fallback := fmt.Sprintf(corporationFallbackName, corpID)
	if w.collected[corpID] {
		w.skip(fallback, ReasonAlreadyCollected)
		// The wallets are already stored, but this character's user must see them.
		for _, id := range w.corpWallets[corpID] {
			w.link(ctx, fallback, id)
		}
		// An earlier character may have lacked the scope or the Director role.
		return w.corporationNames(ctx, ch, corpID, fallback, getToken)
	}
	tok, ok := getToken()
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	divisions, err := w.c.deps.ESI.CorporationWallets(ctx, tok, corpID)
	if esi.IsForbidden(err) {
		w.skip(fallback, ReasonMissingRole)
		return nil
	}
	if err != nil {
		return w.esiFailure(ctx, fallback, err)
	}
	w.collected[corpID] = true

	name, nameErr := w.c.deps.ESI.CorporationName(ctx, corpID)
	if nameErr != nil || name == "" {
		name = fallback
	}
	if err := w.corporation(ctx, corpID, name, tok, divisions); err != nil {
		return err
	}
	if nameErr != nil {
		// The data is kept; only a rate limit ends the run.
		if err := w.esiFailure(ctx, name, nameErr, true); err != nil {
			return err
		}
	}
	return w.corporationNames(ctx, ch, corpID, name, getToken)
}

// corporationNames refreshes the division names of a corporation once per run,
// when the pass wants them and the character has the optional scope. Without
// the scope nothing happens and nothing is recorded. A 403 is a recorded skip
// that a later character of the corporation may still resolve; a failed call
// leaves the stored names untouched.
func (w *walker) corporationNames(ctx context.Context, ch auth.Character, corpID int64, owner string, getToken func() (string, bool)) error {
	if w.names == nil || w.named[corpID] || !slices.Contains(ch.Scopes, ScopeCorporationNames) {
		return nil
	}
	tok, ok := getToken()
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := w.c.deps.ESI.CorporationDivisions(ctx, tok, corpID)
	if esi.IsForbidden(err) {
		w.skip(owner, ReasonMissingDirector)
		return nil
	}
	if err != nil {
		return w.esiFailure(ctx, owner, err)
	}
	w.named[corpID] = true
	return w.names(ctx, corpID, owner, tok, names)
}

// esiFailure classifies an ESI error. A rate limit is recorded in the report
// and stops the run; a context error propagates; anything else is recorded as
// an item error and collection continues (nil). When quiet is set, ordinary
// errors are ignored (used for the optional corporation name lookup).
func (w *walker) esiFailure(ctx context.Context, owner string, err error, quiet ...bool) error {
	var rl *esi.RateLimitError
	if errors.As(err, &rl) {
		w.limited = true
		w.retry = rl.RetryAfter
		return errStop
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if len(quiet) == 0 || !quiet[0] {
		w.fail(owner, err)
	}
	return nil
}

// record stores the wallet and its snapshot, noting it in rep.
// It returns the wallet id and whether the wallet was stored.
func (w *walker) record(ctx context.Context, rep *Report, wl store.Wallet, cents int64) (int64, bool) {
	id, err := w.c.deps.Store.UpsertWallet(ctx, wl)
	if err == nil {
		err = w.c.deps.Store.AddSnapshot(ctx, id, rep.TakenAt, cents)
	}
	if err != nil {
		w.fail(wl.OwnerName, err)
		return 0, false
	}
	w.stored(ctx, wl, id, wl.OwnerName)
	rep.Snapshots = append(rep.Snapshots, Snapshot{
		Kind: wl.Kind, OwnerID: wl.OwnerID, OwnerName: wl.OwnerName, Division: wl.Division, Cents: cents,
	})
	return id, true
}

// stored notes a wallet that now exists in the store: corporation wallets are
// remembered for later characters, and the current character's user is linked.
func (w *walker) stored(ctx context.Context, wl store.Wallet, id int64, label string) {
	if wl.Kind == store.KindCorporation && !slices.Contains(w.corpWallets[wl.OwnerID], id) {
		w.corpWallets[wl.OwnerID] = append(w.corpWallets[wl.OwnerID], id)
	}
	w.link(ctx, label, id)
}

// link makes the current character's user see the wallet. A character without
// a user (UserID 0) links nothing. A failure is recorded like any store error.
func (w *walker) link(ctx context.Context, label string, walletID int64) {
	user := w.cur.UserID
	if user == 0 || w.linked[[2]int64{user, walletID}] {
		return
	}
	if err := w.c.deps.Store.LinkWallet(ctx, user, walletID); err != nil {
		w.fail(label, err)
		return
	}
	w.linked[[2]int64{user, walletID}] = true
}

func (w *walker) skip(owner, reason string) {
	w.skipped = append(w.skipped, Skip{Owner: owner, Reason: reason})
}

func (w *walker) fail(owner string, err error) {
	w.errors = append(w.errors, ItemError{Owner: owner, Err: err})
}
