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
