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
	ReasonMissingDirector   = "cannot read division names (needs the Director role); default names are shown"
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
	// AddJournalEntries stores journal rows and returns how many were new.
	AddJournalEntries(ctx context.Context, walletID int64, entries []store.JournalEntry) (int, error)
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

// Skip records something that was deliberately not collected. OwnerKind and
// OwnerID say whose item it is (character or corporation), so the status
// endpoint can scope it to the users who can see that owner. UserID is the
// user whose character produced the skip (0 when the character has no user).
type Skip struct {
	OwnerKind store.Kind
	OwnerID   int64
	Owner     string
	Reason    string
	UserID    int64
}

// ItemError records a failure for one owner, or a run-level failure when it
// has no owner kind (the collector process itself, not a user). It never
// contains a token.
type ItemError struct {
	OwnerKind store.Kind
	OwnerID   int64
	Owner     string
	Err       error
}

func (e ItemError) Error() string {
	if e.Owner == "" {
		return e.Err.Error() // a run-level error has no owner to prefix
	}
	return fmt.Sprintf("%s: %v", e.Owner, e.Err)
}

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
	// verified holds, per corporation, the users with a character that proved
	// it can read the corporation wallets in this pass.
	verified map[int64]map[int64]bool
	linked   map[[2]int64]bool // {user id, wallet id} pairs already linked
	named    map[int64]bool    // corporations whose division names were fetched
	// corpName is the real name of each corporation resolved in this pass; a
	// corporation without an entry is labelled with the fallback.
	corpName map[int64]string
	seenSkip map[skipKey]bool // skips already recorded in this pass
	skipped  []Skip
	errors   []ItemError
	limited  bool
	retry    time.Duration

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
		corpName:    make(map[int64]string),
		seenSkip:    make(map[skipKey]bool),
		corpWallets: make(map[int64][]int64),
		verified:    make(map[int64]map[int64]bool),
		linked:      make(map[[2]int64]bool),
	}
}

// skipKey identifies a skip within a pass. The user is part of it so that
// every user whose character hit the problem still sees it.
type skipKey struct {
	kind   store.Kind
	id     int64
	reason string
	user   int64
}

// corpLabel returns the real name of a corporation when it is known, else the
// fallback label.
func (w *walker) corpLabel(corpID int64) string {
	if n := w.corpName[corpID]; n != "" {
		return n
	}
	return fmt.Sprintf(corporationFallbackName, corpID)
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
			return w.esiFailure(ctx, store.KindCharacter, ch.ID, ch.Name, err)
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
				w.fail(store.KindCorporation, corpID, name, err)
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
			w.fail(store.KindCharacter, ch.ID, ch.Name, err)
			return "", false
		}
		token = t
		return t, true
	}

	if !slices.Contains(ch.Scopes, ScopeCharacterWallet) {
		w.skip(store.KindCharacter, ch.ID, ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCharacterWallet))
	} else if tok, ok := getToken(); ok {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.personal(ctx, ch, tok); err != nil {
			return err
		}
	}

	if !slices.Contains(ch.Scopes, ScopeCorporationWallet) {
		w.skip(store.KindCharacter, ch.ID, ch.Name, fmt.Sprintf(reasonMissingScopeFmt, ScopeCorporationWallet))
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
		return w.esiFailure(ctx, store.KindCharacter, ch.ID, ch.Name, err)
	}
	if w.collected[corpID] {
		label := w.corpLabel(corpID)
		if user := ch.UserID; user != 0 && !w.verified[corpID][user] {
			// The wallets are stored, but another user's character only gets to
			// see them after proving its own access with its own token.
			ok, err := w.verifyAccess(ctx, ch, corpID, label, getToken)
			if err != nil || !ok {
				return err
			}
		}
		// An earlier character may have lacked the scope or the Director role.
		return w.corporationNames(ctx, ch, corpID, label, getToken)
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
		w.skip(store.KindCorporation, corpID, w.corpLabel(corpID), ReasonMissingRole)
		return nil
	}
	if err != nil {
		return w.esiFailure(ctx, store.KindCorporation, corpID, w.corpLabel(corpID), err)
	}
	w.collected[corpID] = true
	w.markVerified(corpID, ch.UserID)

	name, nameErr := w.c.deps.ESI.CorporationName(ctx, corpID)
	if nameErr != nil || name == "" {
		name = w.corpLabel(corpID)
	} else {
		w.corpName[corpID] = name
	}
	if err := w.corporation(ctx, corpID, name, tok, divisions); err != nil {
		return err
	}
	if nameErr != nil {
		// The data is kept; only a rate limit ends the run.
		if err := w.esiFailure(ctx, store.KindCorporation, corpID, name, nameErr, true); err != nil {
			return err
		}
	}
	return w.corporationNames(ctx, ch, corpID, name, getToken)
}

// verifyAccess checks, with the character's own token, that it can read the
// wallets of an already collected corporation, and links its user to the stored
// wallets when it can. It returns false (and a nil error unless the run must
// end) when the character was not linked: a 403 is the recorded skip
// ReasonMissingRole, any other failure an ordinary ESI error.
func (w *walker) verifyAccess(ctx context.Context, ch auth.Character, corpID int64, owner string, getToken func() (string, bool)) (bool, error) {
	tok, ok := getToken()
	if !ok {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := w.c.deps.ESI.CorporationWallets(ctx, tok, corpID)
	if esi.IsForbidden(err) {
		w.skip(store.KindCorporation, corpID, owner, ReasonMissingRole)
		return false, nil
	}
	if err != nil {
		return false, w.esiFailure(ctx, store.KindCorporation, corpID, owner, err)
	}
	w.markVerified(corpID, ch.UserID)
	for _, id := range w.corpWallets[corpID] {
		w.link(ctx, store.KindCorporation, corpID, owner, id)
	}
	return true, nil
}

// markVerified notes that a character of the user can read the corporation.
func (w *walker) markVerified(corpID, userID int64) {
	if userID == 0 {
		return
	}
	if w.verified[corpID] == nil {
		w.verified[corpID] = make(map[int64]bool)
	}
	w.verified[corpID][userID] = true
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
		w.skip(store.KindCorporation, corpID, owner, ReasonMissingDirector)
		return nil
	}
	if err != nil {
		return w.esiFailure(ctx, store.KindCorporation, corpID, owner, err)
	}
	w.named[corpID] = true
	return w.names(ctx, corpID, owner, tok, names)
}

// esiFailure classifies an ESI error. A rate limit is recorded in the report
// and stops the run; a context error propagates; anything else is recorded as
// an item error and collection continues (nil). When quiet is set, ordinary
// errors are ignored (used for the optional corporation name lookup).
func (w *walker) esiFailure(ctx context.Context, kind store.Kind, ownerID int64, owner string, err error, quiet ...bool) error {
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
		w.fail(kind, ownerID, owner, err)
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
		w.fail(wl.Kind, wl.OwnerID, wl.OwnerName, err)
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
	w.link(ctx, wl.Kind, wl.OwnerID, label, id)
}

// link makes the current character's user see the wallet. A character without
// a user (UserID 0) links nothing. A failure is recorded like any store error.
func (w *walker) link(ctx context.Context, kind store.Kind, ownerID int64, label string, walletID int64) {
	user := w.cur.UserID
	if user == 0 || w.linked[[2]int64{user, walletID}] {
		return
	}
	if err := w.c.deps.Store.LinkWallet(ctx, user, walletID); err != nil {
		w.fail(kind, ownerID, label, err)
		return
	}
	w.linked[[2]int64{user, walletID}] = true
}

// skip records one skip per (owner, reason, user) in a pass: further
// characters of the same user hitting the same problem add nothing.
func (w *walker) skip(kind store.Kind, ownerID int64, owner, reason string) {
	key := skipKey{kind: kind, id: ownerID, reason: reason, user: w.cur.UserID}
	if w.seenSkip[key] {
		return
	}
	w.seenSkip[key] = true
	w.skipped = append(w.skipped, Skip{OwnerKind: kind, OwnerID: ownerID, Owner: owner, Reason: reason, UserID: w.cur.UserID})
}

func (w *walker) fail(kind store.Kind, ownerID int64, owner string, err error) {
	w.errors = append(w.errors, ItemError{OwnerKind: kind, OwnerID: ownerID, Owner: owner, Err: err})
}
