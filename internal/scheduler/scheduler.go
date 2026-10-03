// Package scheduler runs a collection job on a fixed cadence in the
// background.
package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
)

// Loop runs Run immediately and then again Every after each run finishes, so
// runs never overlap. When a report says it was rate limited, the next run is
// delayed by its RetryAfter if that is longer than Every.
type Loop struct {
	// Every is the pause between the end of a run and the start of the next.
	Every time.Duration
	// Run performs one collection.
	Run func(ctx context.Context) (collector.Report, error)
	// OnResult receives the outcome of every run. It may be nil.
	OnResult func(collector.Report, error)
	// Wait pauses for d or until ctx ends, returning ctx's error in that case.
	// It defaults to a timer; tests inject a fake.
	Wait func(ctx context.Context, d time.Duration) error
}

// Start runs the loop until ctx is cancelled and then returns nil. It returns
// an error right away when the loop is misconfigured. A failed run is reported
// to OnResult and never stops the loop.
func (l *Loop) Start(ctx context.Context) error {
	if l.Every <= 0 {
		return errors.New("scheduler: Every must be positive")
	}
	if l.Run == nil {
		return errors.New("scheduler: Run is required")
	}
	wait := l.Wait
	if wait == nil {
		wait = sleep
	}
	for {
		rep, err := l.Run(ctx)
		if l.OnResult != nil {
			l.OnResult(rep, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := l.Every
		if rep.RateLimited && rep.RetryAfter > delay {
			delay = rep.RetryAfter
		}
		if wait(ctx, delay) != nil {
			return nil
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
