package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestMySQLRestoreClaimSupervisorRenewsBeforeWorkAndCancelsOnLeaseLoss(t *testing.T) {
	leaseLost := errors.New("claim ownership lost")
	var calls atomic.Int32
	started := make(chan struct{})
	executorDone := make(chan struct{})
	supervisor, err := newMySQLRestoreClaimSupervisor(context.Background(), 45*time.Millisecond, func(ctx context.Context, lease time.Duration) error {
		if lease != 45*time.Millisecond {
			t.Errorf("lease = %s", lease)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("renewal is unbounded")
		}
		if calls.Add(1) == 1 {
			return nil // Initial proof, before the SQL client may start.
		}
		return leaseLost
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(); err != nil {
		t.Fatalf("initial renewal: %v", err)
	}
	go func() {
		close(started)
		<-supervisor.Context().Done()
		close(executorDone)
	}()
	<-started
	select {
	case <-executorDone:
	case <-time.After(time.Second):
		t.Fatal("lease loss did not cancel SQL context")
	}
	if err := supervisor.Stop(); !errors.Is(err, leaseLost) {
		t.Fatalf("supervisor result = %v, want lease-loss error", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("renewal calls = %d, want initial plus periodic renewal", calls.Load())
	}
}

func TestMySQLRestoreClaimSupervisorRejectsMissingLeaseOrRenewer(t *testing.T) {
	if _, err := newMySQLRestoreClaimSupervisor(context.Background(), 0, func(context.Context, time.Duration) error { return nil }); err == nil {
		t.Fatal("zero lease accepted")
	}
	if _, err := newMySQLRestoreClaimSupervisor(context.Background(), time.Second, nil); err == nil {
		t.Fatal("nil renewer accepted")
	}
}

func TestMySQLRestoreClaimSupervisorStopsCleanlyBeforeNextRenewal(t *testing.T) {
	var calls atomic.Int32
	supervisor, err := newMySQLRestoreClaimSupervisor(context.Background(), time.Second, func(context.Context, time.Duration) error {
		calls.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("renewals = %d, want only initial renewal", calls.Load())
	}
}
