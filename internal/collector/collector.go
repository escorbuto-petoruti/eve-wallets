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
}

// StoreWriter is the subset of *store.Store the collector needs.
type StoreWriter interface {
	UpsertWallet(ctx context.Context, w store.Wallet) (int64, error)
	AddSnapshot(ctx context.Context, walletID int64, takenAt time.Time, cents int64) error
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

// run holds the state of one collection.
type run struct {
	c         *Collector
	takenAt   time.Time
	rep       Report
	collected map[int64]bool // corporations already snapshotted
}

// errStop is an internal signal that the run must end now.
var errStop = errors.New("collector: stop")

// Run collects one snapshot of every wallet of every character. It returns an
// error only when the run cannot start (the character list is unavailable) or
// the context ends; per-item failures are in the Report.
func (c *Collector) Run(ctx context.Context) (Report, error) {
	chars, err := c.deps.Auth.Characters(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("collector: list characters: %w", err)
	}
	r := &run{
		c:         c,
		takenAt:   c.deps.Now().Truncate(time.Second),
		collected: make(map[int64]bool),
	}
	r.rep.TakenAt = r.takenAt
	for _, ch := range chars {
		if err := ctx.Err(); err != nil {
			return r.rep, err
		}
		if err := r.character(ctx, ch); err != nil {
			if errors.Is(err, errStop) {
				break
			}
			return r.rep, err
		}
	}
	return r.rep, ctx.Err()
}

// character collects the wallets of one character. It returns errStop after a
// rate limit and a context error when the context ends.
func (r *run) character(ctx context.Context, ch auth.Character) error {
	var token string
	tokenFailed := false
	getToken := func() (string, bool) {
		if token != "" {
			return token, true
		}
		if tokenFailed {
			return "", false
		}
		t, err := r.c.deps.Auth.Token(ctx, ch.ID)
		if err != nil {
			tokenFailed = true
			r.fail(ch.Name, err)
			return "", false
		}
		token = t
		return t, true
	}

	if !slices.Contains(ch.Scopes, ScopeCharacterWallet) {
		r.skip(ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCharacterWallet))
	} else if tok, ok := getToken(); ok {
		if err := r.personal(ctx, ch, tok); err != nil {
			return err
		}
	}

	if !slices.Contains(ch.Scopes, ScopeCorporationWallet) {
		r.skip(ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCorporationWallet))
		return nil
	}
	return r.corporation(ctx, ch, getToken)
}

func (r *run) personal(ctx context.Context, ch auth.Character, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cents, err := r.c.deps.ESI.CharacterWallet(ctx, token, ch.ID)
	if err != nil {
		return r.esiFailure(ctx, ch.Name, err)
	}
	r.record(ctx, store.Wallet{Kind: store.KindCharacter, OwnerID: ch.ID, OwnerName: ch.Name}, cents)
	return nil
}

func (r *run) corporation(ctx context.Context, ch auth.Character, getToken func() (string, bool)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	corpID, err := r.c.deps.ESI.CharacterCorporationID(ctx, ch.ID)
	if err != nil {
		return r.esiFailure(ctx, ch.Name, err)
	}
	fallback := fmt.Sprintf(corporationFallbackName, corpID)
	if r.collected[corpID] {
		r.skip(fallback, ReasonAlreadyCollected)
		return nil
	}
	tok, ok := getToken()
	if !ok {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	divisions, err := r.c.deps.ESI.CorporationWallets(ctx, tok, corpID)
	if esi.IsForbidden(err) {
		r.skip(fallback, ReasonMissingRole)
		return nil
	}
	if err != nil {
		return r.esiFailure(ctx, fallback, err)
	}
	r.collected[corpID] = true

	name, nameErr := r.c.deps.ESI.CorporationName(ctx, corpID)
	if nameErr != nil || name == "" {
		name = fallback
	}
	for _, d := range divisions {
		r.record(ctx, store.Wallet{Kind: store.KindCorporation, OwnerID: corpID, OwnerName: name, Division: d.Division}, d.Cents)
	}
	if nameErr != nil {
		// The data is kept; only a rate limit ends the run.
		return r.esiFailure(ctx, name, nameErr, true)
	}
	return nil
}

// esiFailure classifies an ESI error. A rate limit is recorded in the report
// and stops the run; a context error propagates; anything else is recorded as
// an item error and collection continues (nil). When quiet is set, ordinary
// errors are ignored (used for the optional corporation name lookup).
func (r *run) esiFailure(ctx context.Context, owner string, err error, quiet ...bool) error {
	var rl *esi.RateLimitError
	if errors.As(err, &rl) {
		r.rep.RateLimited = true
		r.rep.RetryAfter = rl.RetryAfter
		return errStop
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if len(quiet) == 0 || !quiet[0] {
		r.fail(owner, err)
	}
	return nil
}

// record stores the wallet and its snapshot, noting it in the report.
func (r *run) record(ctx context.Context, w store.Wallet, cents int64) {
	id, err := r.c.deps.Store.UpsertWallet(ctx, w)
	if err == nil {
		err = r.c.deps.Store.AddSnapshot(ctx, id, r.takenAt, cents)
	}
	if err != nil {
		r.fail(w.OwnerName, err)
		return
	}
	r.rep.Snapshots = append(r.rep.Snapshots, Snapshot{
		Kind: w.Kind, OwnerID: w.OwnerID, OwnerName: w.OwnerName, Division: w.Division, Cents: cents,
	})
}

func (r *run) skip(owner, reason string) {
	r.rep.Skipped = append(r.rep.Skipped, Skip{Owner: owner, Reason: reason})
}

func (r *run) fail(owner string, err error) {
	r.rep.Errors = append(r.rep.Errors, ItemError{Owner: owner, Err: err})
}
