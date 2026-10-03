package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/escorbuto-petoruti/eve-wallets/internal/collector"
	"github.com/escorbuto-petoruti/eve-wallets/internal/store"
)

// fakeCycler records the order of the calls and returns canned outcomes.
type fakeCycler struct {
	calls     []string
	runRep    collector.Report
	runErr    error
	backRep   collector.BackfillReport
	backErr   error
	onRun     func()
	onBackfil func()
}

func (f *fakeCycler) Run(context.Context) (collector.Report, error) {
	f.calls = append(f.calls, "run")
	if f.onRun != nil {
		f.onRun()
	}
	return f.runRep, f.runErr
}

func (f *fakeCycler) Backfill(context.Context) (collector.BackfillReport, error) {
	f.calls = append(f.calls, "backfill")
	if f.onBackfil != nil {
		f.onBackfil()
	}
	return f.backRep, f.backErr
}

func TestCycleOrderSnapshotThenBackfill(t *testing.T) {
	f := &fakeCycler{runRep: collector.Report{Snapshots: make([]collector.Snapshot, 2)}}
	rep, err := newCycle(f, true)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"run", "backfill"}; !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls = %v, want %v", f.calls, want)
	}
	if len(rep.Snapshots) != 2 {
		t.Errorf("snapshots = %d, want 2", len(rep.Snapshots))
	}
}

func TestCycleSkipsBackfill(t *testing.T) {
	tests := []struct {
		name     string
		backfill bool
		runRep   collector.Report
		cancel   bool
	}{
		{name: "snapshot rate limited", backfill: true, runRep: collector.Report{RateLimited: true, RetryAfter: time.Minute}},
		{name: "no-backfill flag", backfill: false},
		{name: "context cancelled during snapshot", backfill: true, cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := &fakeCycler{runRep: tt.runRep}
			if tt.cancel {
				f.onRun = cancel
			}
			rep, err := newCycle(f, tt.backfill)(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{"run"}; !reflect.DeepEqual(f.calls, want) {
				t.Errorf("calls = %v, want %v", f.calls, want)
			}
			if rep.JournalPoints != 0 {
				t.Errorf("journal points = %d, want 0", rep.JournalPoints)
			}
			if rep.RateLimited != tt.runRep.RateLimited || rep.RetryAfter != tt.runRep.RetryAfter {
				t.Errorf("rate limit info changed: %+v", rep)
			}
		})
	}
}

func TestCycleSnapshotFatalErrorSkipsBackfill(t *testing.T) {
	boom := errors.New("auth: boom")
	f := &fakeCycler{runErr: boom}
	_, err := newCycle(f, true)(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if want := []string{"run"}; !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

func TestCycleBackfillFatalErrorKeepsSnapshot(t *testing.T) {
	f := &fakeCycler{
		runRep:  collector.Report{Snapshots: make([]collector.Snapshot, 3)},
		backErr: errors.New("list characters: boom"),
	}
	rep, err := newCycle(f, true)(context.Background())
	if err != nil {
		t.Fatalf("a backfill failure must not fail the cycle: %v", err)
	}
	if len(rep.Snapshots) != 3 {
		t.Errorf("snapshots = %d, want 3", len(rep.Snapshots))
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Error() != "backfill: list characters: boom" {
		t.Errorf("errors = %v", rep.Errors)
	}
}

func TestCycleBackfillCancelledAddsNoError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeCycler{backErr: context.Canceled, onBackfil: cancel}
	rep, err := newCycle(f, true)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %v, want none on shutdown", rep.Errors)
	}
}

func TestCycleMergesReports(t *testing.T) {
	taken := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	f := &fakeCycler{
		runRep: collector.Report{
			TakenAt:      taken,
			Snapshots:    make([]collector.Snapshot, 1),
			Skipped:      []collector.Skip{{Owner: "Corp", Reason: "snap skip"}},
			Errors:       []collector.ItemError{{Owner: "Alice", Err: errors.New("snap boom")}},
			RetryAfter:   30 * time.Second,
			NamesUpdated: 4,
		},
		backRep: collector.BackfillReport{
			Wallets: []collector.BackfillWallet{
				{Kind: store.KindCharacter, Points: 5},
				{Kind: store.KindCorporation, Points: 7, NoBalance: 2},
			},
			Skipped:     []collector.Skip{{Owner: "Corp", Reason: "back skip"}},
			Errors:      []collector.ItemError{{Owner: "Bob", Err: errors.New("back boom")}},
			RateLimited: true,
			RetryAfter:  2 * time.Minute,
		},
	}
	rep, err := newCycle(f, true)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.TakenAt.Equal(taken) || len(rep.Snapshots) != 1 || rep.NamesUpdated != 4 {
		t.Errorf("snapshot fields lost: %+v", rep)
	}
	wantSkips := []collector.Skip{{Owner: "Corp", Reason: "snap skip"}, {Owner: "Corp", Reason: "back skip"}}
	if !reflect.DeepEqual(rep.Skipped, wantSkips) {
		t.Errorf("skipped = %v, want %v", rep.Skipped, wantSkips)
	}
	var errs []string
	for _, e := range rep.Errors {
		errs = append(errs, e.Error())
	}
	if want := "Alice: snap boom|backfill: Bob: back boom"; strings.Join(errs, "|") != want {
		t.Errorf("errors = %q, want %q", strings.Join(errs, "|"), want)
	}
	if !rep.RateLimited || rep.RetryAfter != 2*time.Minute {
		t.Errorf("rate limit = %v/%v, want true/2m", rep.RateLimited, rep.RetryAfter)
	}
	if rep.JournalPoints != 12 {
		t.Errorf("journal points = %d, want 12", rep.JournalPoints)
	}
}

func TestCycleKeepsLargerSnapshotRetryAfter(t *testing.T) {
	f := &fakeCycler{
		runRep:  collector.Report{RetryAfter: 5 * time.Minute},
		backRep: collector.BackfillReport{RateLimited: true, RetryAfter: time.Minute},
	}
	rep, _ := newCycle(f, true)(context.Background())
	if rep.RetryAfter != 5*time.Minute || !rep.RateLimited {
		t.Errorf("rate limit = %v/%v, want true/5m", rep.RateLimited, rep.RetryAfter)
	}
}

// The merge must keep the owner identity of backfill errors, not rebuild
// identity-less ones, so the status endpoint can scope them to the user.
func TestMergeBackfillKeepsOwnerIdentity(t *testing.T) {
	f := &fakeCycler{backRep: collector.BackfillReport{
		Skipped: []collector.Skip{{OwnerKind: store.KindCorporation, OwnerID: 900, Owner: "Acme", Reason: "missing corporation role"}},
		Errors:  []collector.ItemError{{OwnerKind: store.KindCorporation, OwnerID: 900, Owner: "Acme", Err: errors.New("back boom")}},
	}}
	rep, err := newCycle(f, true)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != (collector.Skip{OwnerKind: store.KindCorporation, OwnerID: 900, Owner: "Acme", Reason: "missing corporation role"}) {
		t.Errorf("skipped = %+v, want the owner identity kept", rep.Skipped)
	}
	if len(rep.Errors) != 1 {
		t.Fatalf("errors = %+v", rep.Errors)
	}
	if e := rep.Errors[0]; e.OwnerKind != store.KindCorporation || e.OwnerID != 900 || e.Owner != "backfill: Acme" || e.Error() != "backfill: Acme: back boom" {
		t.Errorf("error = %+v, want the owner identity kept and the backfill prefix", e)
	}
}
