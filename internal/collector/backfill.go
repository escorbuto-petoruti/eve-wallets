package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/auth"
	"github.com/escorbuto-petoruti/eve-wallets/internal/esi"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// BackfillWallet is the outcome for one wallet journal.
type BackfillWallet struct {
	Kind      store.Kind
	OwnerID   int64
	OwnerName string
	Division  int
	// Points is the number of journal entries whose balance was stored or was
	// already stored; NoBalance counts entries ESI returned without a balance.
	Points    int
	NoBalance int
}

// BackfillReport is the outcome of one backfill. It never contains a token.
type BackfillReport struct {
	Wallets []BackfillWallet
	Skipped []Skip
	Errors  []ItemError
	// RateLimited is true when ESI rate limiting cut the run short; the report
	// then holds only what was processed before that. RetryAfter is ESI's hint.
	RateLimited bool
	RetryAfter  time.Duration
}

// Points returns the journal points stored (or already present) overall.
func (r BackfillReport) Points() int {
	n := 0
	for _, w := range r.Wallets {
		n += w.Points
	}
	return n
}

// NoBalance returns the journal entries skipped for lacking a balance.
func (r BackfillReport) NoBalance() int {
	n := 0
	for _, w := range r.Wallets {
		n += w.NoBalance
	}
	return n
}

// Backfill stores the running balance of every wallet journal entry ESI still
// holds (about 30 days) as a historical point. It follows the same discovery
// rules as Run and is idempotent: repeating it changes nothing. It returns an
// error only when the character list is unavailable or the context ends.
func (c *Collector) Backfill(ctx context.Context) (BackfillReport, error) {
	w := newWalker(c)
	var rep BackfillReport
	w.personal = func(ctx context.Context, ch auth.Character, token string) error {
		entries, err := c.deps.ESI.CharacterJournal(ctx, token, ch.ID)
		if err != nil {
			return w.esiFailure(ctx, ch.Name, err)
		}
		w.backfillWallet(ctx, &rep, store.Wallet{Kind: store.KindCharacter, OwnerID: ch.ID, OwnerName: ch.Name}, entries)
		return nil
	}
	w.corporation = func(ctx context.Context, corpID int64, name, token string, divisions []esi.DivisionBalance) error {
		for _, d := range divisions {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := c.deps.ESI.CorporationJournal(ctx, token, corpID, d.Division)
			if esi.IsForbidden(err) {
				w.skip(fmt.Sprintf("%s (division %d)", name, d.Division), ReasonMissingRole)
				continue
			}
			if err != nil {
				if err := w.esiFailure(ctx, fmt.Sprintf("%s (division %d)", name, d.Division), err); err != nil {
					return err
				}
				continue
			}
			wl := store.Wallet{Kind: store.KindCorporation, OwnerID: corpID, OwnerName: name, Division: d.Division}
			w.backfillWallet(ctx, &rep, wl, entries)
		}
		return nil
	}
	err := w.walk(ctx)
	rep.Skipped, rep.Errors = w.skipped, w.errors
	rep.RateLimited, rep.RetryAfter = w.limited, w.retry
	if err != nil && ctx.Err() == nil {
		return BackfillReport{}, err // the character list is unavailable
	}
	return rep, err
}

// backfillWallet makes sure the wallet exists and stores the balance of every
// entry that carries one. A store failure is recorded and ends this wallet.
func (w *walker) backfillWallet(ctx context.Context, rep *BackfillReport, wl store.Wallet, entries []esi.JournalEntry) {
	label := wl.OwnerName
	if wl.Kind == store.KindCorporation {
		label = fmt.Sprintf("%s (division %d)", wl.OwnerName, wl.Division)
	}
	id, err := w.c.deps.Store.UpsertWallet(ctx, wl)
	if err != nil {
		w.fail(label, err)
		return
	}
	w.stored(ctx, wl, id, label)
	out := BackfillWallet{Kind: wl.Kind, OwnerID: wl.OwnerID, OwnerName: wl.OwnerName, Division: wl.Division}
	for _, e := range entries {
		if e.BalanceCents == nil {
			out.NoBalance++
			continue
		}
		if err := w.c.deps.Store.AddJournalBalance(ctx, id, e.ID, e.Date, *e.BalanceCents); err != nil {
			w.fail(label, err)
			break
		}
		out.Points++
	}
	rep.Wallets = append(rep.Wallets, out)
}
