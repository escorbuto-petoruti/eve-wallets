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

	ReasonMissingRole       = "missing corporation role"
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
	CharacterJournal(ctx context.Context, token string, characterID int64) ([]esi.JournalEntry, error)
	CorporationJournal(ctx context.Context, token string, corporationID int64, division int) ([]esi.JournalEntry, error)
}

// StoreWriter is the subset of *store.Store the collector needs.
type StoreWriter interface {
	UpsertWallet(ctx context.Context, w store.Wallet) (int64, error)
	AddSnapshot(ctx context.Context, walletID int64, takenAt time.Time, cents int64) error
	AddJournalBalance(ctx context.Context, walletID, entryID int64, at time.Time, cents int64) error
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
	skipped   []Skip
	errors    []ItemError
	limited   bool
	retry     time.Duration

	// personal handles the personal wallet of ch.
	personal func(ctx context.Context, ch auth.Character, token string) error
	// corporation handles one corporation the character can read, with the
	// divisions ESI returned for it.
	corporation func(ctx context.Context, corpID int64, name, token string, divisions []esi.DivisionBalance) error
}

func newWalker(c *Collector) *walker {
	return &walker{c: c, collected: make(map[int64]bool)}
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
	w.corporation = func(ctx context.Context, corpID int64, name, _ string, divisions []esi.DivisionBalance) error {
		for _, d := range divisions {
			w.record(ctx, &rep, store.Wallet{Kind: store.KindCorporation, OwnerID: corpID, OwnerName: name, Division: d.Division}, d.Cents)
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
		return nil
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
		return w.esiFailure(ctx, name, nameErr, true)
	}
	return nil
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
func (w *walker) record(ctx context.Context, rep *Report, wl store.Wallet, cents int64) {
	id, err := w.c.deps.Store.UpsertWallet(ctx, wl)
	if err == nil {
		err = w.c.deps.Store.AddSnapshot(ctx, id, rep.TakenAt, cents)
	}
	if err != nil {
		w.fail(wl.OwnerName, err)
		return
	}
	rep.Snapshots = append(rep.Snapshots, Snapshot{
		Kind: wl.Kind, OwnerID: wl.OwnerID, OwnerName: wl.OwnerName, Division: wl.Division, Cents: cents,
	})
}

func (w *walker) skip(owner, reason string) {
	w.skipped = append(w.skipped, Skip{Owner: owner, Reason: reason})
}

func (w *walker) fail(owner string, err error) {
	w.errors = append(w.errors, ItemError{Owner: owner, Err: err})
}
