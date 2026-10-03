package main

import (
	"context"

	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
)

// cycler is what one serve cycle needs from the collector.
type cycler interface {
	Run(ctx context.Context) (collector.Report, error)
	Backfill(ctx context.Context) (collector.BackfillReport, error)
}

// newCycle returns the job of one serve cycle: a snapshot and, when backfill
// is true, a journal backfill. The backfill is skipped when the snapshot
// failed, was rate limited or the context ended. A backfill failure is folded
// into the report and never discards the snapshot or fails the cycle.
func newCycle(c cycler, backfill bool) func(context.Context) (collector.Report, error) {
	return func(ctx context.Context) (collector.Report, error) {
		rep, err := c.Run(ctx)
		if err != nil || !backfill || rep.RateLimited || ctx.Err() != nil {
			return rep, err
		}
		back, err := c.Backfill(ctx)
		return mergeBackfill(rep, back, err, ctx.Err() == nil), nil
	}
}

// mergeBackfill adds the outcome of a backfill to the snapshot report rep.
// Backfill errors are prefixed "backfill: " so they stay attributable. A
// failure of the whole backfill (err) is recorded only when report is true,
// that is, when it is not just the shutdown of the program.
func mergeBackfill(rep collector.Report, back collector.BackfillReport, err error, report bool) collector.Report {
	rep.Skipped = append(rep.Skipped, back.Skipped...)
	for _, e := range back.Errors {
		rep.Errors = append(rep.Errors, collector.ItemError{Owner: "backfill: " + e.Owner, Err: e.Err})
	}
	if err != nil && report {
		rep.Errors = append(rep.Errors, collector.ItemError{Owner: "backfill", Err: err})
	}
	rep.RateLimited = rep.RateLimited || back.RateLimited
	rep.RetryAfter = max(rep.RetryAfter, back.RetryAfter)
	rep.JournalPoints = back.Points()
	return rep
}
