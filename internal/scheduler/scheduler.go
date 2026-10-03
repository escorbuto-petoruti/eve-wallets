// Package scheduler runs a collection job on a fixed cadence in the
// background.
package scheduler

import (
	"context"
	"errors"
	"sync"
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

	once    sync.Once
	trigger chan struct{}
}

func (l *Loop) triggers() chan struct{} {
	l.once.Do(func() { l.trigger = make(chan struct{}, 1) })
	return l.trigger
}

// Trigger asks for a run as soon as the loop is idle. It never blocks and is
// safe to call from any goroutine, before or after Start: a run in progress is
// never overlapped and repeated triggers collapse into a single extra run.
// Triggers are ignored while the loop is backing off after a rate limit.
func (l *Loop) Trigger() {
	select {
	case l.triggers() <- struct{}{}:
	default:
	}
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
	trig := l.triggers()
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
		if rep.RateLimited {
			select { // drop a pending trigger: do not hammer a limited API
			case <-trig:
			default:
			}
			if wait(ctx, delay) != nil {
				return nil
			}
			continue
		}
		select {
		case <-trig: // asked while the run was in progress: run again now
			continue
		default:
		}
		if !pause(ctx, wait, delay, trig) {
			return nil
		}
	}
}

// pause waits delay, ending early when a trigger arrives. It reports whether
// the loop should run again (false when ctx ended or the wait failed).
func pause(ctx context.Context, wait func(context.Context, time.Duration) error, delay time.Duration, trig <-chan struct{}) bool {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var triggered bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-trig:
			triggered = true
			cancel()
		case <-wctx.Done():
		}
	}()
	err := wait(wctx, delay)
	cancel()
	<-done
	if ctx.Err() != nil {
		return false
	}
	return err == nil || triggered
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
