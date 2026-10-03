package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
)

// fakeWait records the requested delays and lets the test drive the loop.
type fakeWait struct {
	delays  chan time.Duration // every wait announces its delay here
	release chan struct{}      // a send lets one wait return
}

func newFakeWait() *fakeWait {
	return &fakeWait{delays: make(chan time.Duration, 100), release: make(chan struct{})}
}

func (f *fakeWait) wait(ctx context.Context, d time.Duration) error {
	f.delays <- d
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
		panic("unreachable")
	}
}

type result struct {
	rep collector.Report
	err error
}

func TestRejectsNonPositiveEvery(t *testing.T) {
	for _, every := range []time.Duration{0, -time.Second} {
		l := &Loop{Every: every, Run: func(context.Context) (collector.Report, error) { return collector.Report{}, nil }}
		if err := l.Start(context.Background()); err == nil {
			t.Errorf("Every=%v: want error", every)
		}
	}
	l := &Loop{Every: time.Minute}
	if err := l.Start(context.Background()); err == nil {
		t.Error("nil Run: want error")
	}
}

func TestRunsImmediatelyThenRepeats(t *testing.T) {
	fw := newFakeWait()
	runs := make(chan int, 10)
	var n int
	l := &Loop{
		Every: time.Minute,
		Run: func(context.Context) (collector.Report, error) {
			n++
			runs <- n
			return collector.Report{}, nil
		},
		Wait: fw.wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()

	if got := recv(t, runs); got != 1 {
		t.Fatalf("first run = %d", got)
	}
	if d := recv(t, fw.delays); d != time.Minute {
		t.Errorf("delay = %v, want 1m", d)
	}
	fw.release <- struct{}{}
	if got := recv(t, runs); got != 2 {
		t.Fatalf("second run = %d", got)
	}
	recv(t, fw.delays)
	cancel()
	if err := recv(t, done); err != nil {
		t.Errorf("Start after cancel = %v, want nil", err)
	}
}

func TestRateLimitDelaysNextRun(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter time.Duration
		want       time.Duration
	}{
		{"retry after longer than every", 10 * time.Minute, 10 * time.Minute},
		{"retry after shorter than every", time.Second, time.Minute},
		{"not rate limited ignores retry after", 0, time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fw := newFakeWait()
			l := &Loop{
				Every: time.Minute,
				Run: func(context.Context) (collector.Report, error) {
					return collector.Report{RateLimited: tt.retryAfter > 0, RetryAfter: tt.retryAfter}, nil
				},
				Wait: fw.wait,
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { _ = l.Start(ctx) }()
			if d := recv(t, fw.delays); d != tt.want {
				t.Errorf("delay = %v, want %v", d, tt.want)
			}
		})
	}
}

func TestNotRateLimitedIgnoresRetryAfter(t *testing.T) {
	fw := newFakeWait()
	l := &Loop{
		Every: time.Minute,
		Run: func(context.Context) (collector.Report, error) {
			return collector.Report{RetryAfter: time.Hour}, nil
		},
		Wait: fw.wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()
	if d := recv(t, fw.delays); d != time.Minute {
		t.Errorf("delay = %v, want 1m", d)
	}
}

func TestErrorDoesNotStopLoop(t *testing.T) {
	fw := newFakeWait()
	boom := errors.New("boom")
	results := make(chan result, 10)
	var n int
	l := &Loop{
		Every: time.Minute,
		Run: func(context.Context) (collector.Report, error) {
			n++
			if n == 1 {
				return collector.Report{}, boom
			}
			return collector.Report{TakenAt: time.Unix(5, 0)}, nil
		},
		OnResult: func(r collector.Report, err error) { results <- result{r, err} },
		Wait:     fw.wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	if r := recv(t, results); !errors.Is(r.err, boom) {
		t.Fatalf("first result err = %v", r.err)
	}
	recv(t, fw.delays)
	fw.release <- struct{}{}
	if r := recv(t, results); r.err != nil || r.rep.TakenAt.Unix() != 5 {
		t.Fatalf("second result = %+v", r)
	}
}

func TestCancelDuringWait(t *testing.T) {
	fw := newFakeWait()
	l := &Loop{
		Every: time.Minute,
		Run:   func(context.Context) (collector.Report, error) { return collector.Report{}, nil },
		Wait:  fw.wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	recv(t, fw.delays)
	cancel()
	if err := recv(t, done); err != nil {
		t.Errorf("Start = %v, want nil", err)
	}
}

func TestCancelDuringRun(t *testing.T) {
	started := make(chan struct{})
	var runs atomic.Int32
	l := &Loop{
		Every: time.Minute,
		Run: func(ctx context.Context) (collector.Report, error) {
			runs.Add(1)
			close(started)
			<-ctx.Done()
			return collector.Report{}, ctx.Err()
		},
		Wait: func(context.Context, time.Duration) error {
			t.Error("must not wait after cancellation")
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Start(ctx) }()
	recv(t, started)
	cancel()
	if err := recv(t, done); err != nil {
		t.Errorf("Start = %v, want nil", err)
	}
	if runs.Load() != 1 {
		t.Errorf("runs = %d", runs.Load())
	}
}

func TestNoOverlap(t *testing.T) {
	var running, maxRunning atomic.Int32
	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	l := &Loop{
		Every: time.Nanosecond,
		Run: func(context.Context) (collector.Report, error) {
			cur := running.Add(1)
			if cur > maxRunning.Load() {
				maxRunning.Store(cur)
			}
			time.Sleep(time.Millisecond)
			running.Add(-1)
			if runs.Add(1) == 5 {
				cancel()
			}
			return collector.Report{}, nil
		},
	}
	go func() { done <- l.Start(ctx) }()
	recv(t, done)
	if maxRunning.Load() != 1 {
		t.Errorf("max concurrent runs = %d, want 1", maxRunning.Load())
	}
	if runs.Load() < 5 {
		t.Errorf("runs = %d, want >= 5", runs.Load())
	}
}

// startLoop runs a loop whose Run reports each start on runs and, when gate is
// not nil, blocks until it receives from gate.
func startLoop(t *testing.T, fw *fakeWait, gate chan struct{}) (*Loop, chan int, context.CancelFunc) {
	t.Helper()
	runs := make(chan int, 20)
	var n atomic.Int32
	l := &Loop{
		Every: time.Hour,
		Run: func(ctx context.Context) (collector.Report, error) {
			runs <- int(n.Add(1))
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
				}
			}
			return collector.Report{}, nil
		},
		Wait: fw.wait,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Start(ctx) }()
	return l, runs, cancel
}

func TestTriggerRunsWithoutWaitingForTheInterval(t *testing.T) {
	fw := newFakeWait()
	l, runs, _ := startLoop(t, fw, nil)
	recv(t, runs)
	recv(t, fw.delays) // waiting for the next interval
	l.Trigger()
	if got := recv(t, runs); got != 2 {
		t.Fatalf("run after trigger = %d, want 2", got)
	}
	if d := recv(t, fw.delays); d != time.Hour {
		t.Errorf("delay after triggered run = %v, want the normal interval", d)
	}
}

func TestTriggerBeforeStartRunsOnceMore(t *testing.T) {
	fw := newFakeWait()
	runs := make(chan int, 10)
	var n int
	l := &Loop{Every: time.Hour, Wait: fw.wait, Run: func(context.Context) (collector.Report, error) {
		n++
		runs <- n
		return collector.Report{}, nil
	}}
	l.Trigger()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()
	recv(t, runs)
	if got := recv(t, runs); got != 2 {
		t.Fatalf("second run = %d, want 2 (pending trigger)", got)
	}
}

func TestTriggerDuringRunDoesNotOverlapAndCoalesces(t *testing.T) {
	fw := newFakeWait()
	gate := make(chan struct{})
	var active, maxActive atomic.Int32
	runs := make(chan int, 20)
	var n atomic.Int32
	l := &Loop{Every: time.Hour, Wait: fw.wait, Run: func(context.Context) (collector.Report, error) {
		cur := active.Add(1)
		if cur > maxActive.Load() {
			maxActive.Store(cur)
		}
		runs <- int(n.Add(1))
		<-gate
		active.Add(-1)
		return collector.Report{}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()

	recv(t, runs)
	for i := 0; i < 5; i++ {
		l.Trigger() // never blocks, even while a run is in progress
	}
	gate <- struct{}{} // first run ends; the five triggers coalesce into one run
	if got := recv(t, runs); got != 2 {
		t.Fatalf("run after coalesced triggers = %d, want 2", got)
	}
	gate <- struct{}{}
	recv(t, fw.delays) // back to waiting: no third run was queued
	select {
	case got := <-runs:
		t.Fatalf("unexpected extra run %d", got)
	case <-time.After(50 * time.Millisecond):
	}
	if maxActive.Load() != 1 {
		t.Errorf("runs overlapped: max concurrent = %d", maxActive.Load())
	}
}

func TestTriggerIsIgnoredWhileRateLimited(t *testing.T) {
	fw := newFakeWait()
	runs := make(chan int, 10)
	var n int
	l := &Loop{Every: time.Minute, Wait: fw.wait, Run: func(context.Context) (collector.Report, error) {
		n++
		runs <- n
		return collector.Report{RateLimited: true, RetryAfter: time.Hour}, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Start(ctx) }()
	recv(t, runs)
	if d := recv(t, fw.delays); d != time.Hour {
		t.Fatalf("delay = %v, want 1h", d)
	}
	l.Trigger()
	select {
	case got := <-runs:
		t.Fatalf("run %d started while rate limited", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTriggerAfterCancelDoesNotBlock(t *testing.T) {
	fw := newFakeWait()
	l, runs, cancel := startLoop(t, fw, nil)
	recv(t, runs)
	recv(t, fw.delays)
	cancel()
	for i := 0; i < 3; i++ {
		l.Trigger()
	}
}
