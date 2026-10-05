package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic Clock: time moves only through Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			kept = append(kept, t)
		}
	}
	c.timers = kept
}

// fakeStore records every reconcile and listing call.
type fakeStore struct {
	mu          sync.Mutex
	names       []string
	calls       []string
	callTimes   []time.Time
	clock       *fakeClock
	listCalls   int
	listSizes   []int
	err         error
	onReconcile func(name string)
}

func (s *fakeStore) ReconcileFleetResource(_ context.Context, name string, derive func(Input) Status, _ func()) (*Resource, error) {
	s.mu.Lock()
	s.calls = append(s.calls, name)
	s.callTimes = append(s.callTimes, s.clock.Now())
	hook, err := s.onReconcile, s.err
	s.mu.Unlock()
	if hook != nil {
		hook(name)
	}
	if err != nil {
		return nil, err
	}
	return &Resource{Name: name, Status: derive(Input{Now: s.clock.Now()})}, nil
}

func (s *fakeStore) ListFleetResourceNames(_ context.Context, after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCalls++
	sorted := append([]string(nil), s.names...)
	sort.Strings(sorted)
	out := []string{}
	for _, n := range sorted {
		if n > after && len(out) < limit {
			out = append(out, n)
		}
	}
	s.listSizes = append(s.listSizes, len(out))
	return out, nil
}

func (s *fakeStore) listCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listCalls
}

func (s *fakeStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func newTestReconciler(store *fakeStore, clock *fakeClock) *Reconciler {
	store.clock = clock
	return &Reconciler{Store: store, Clock: clock}
}

func TestReconcilerNotifyDedups(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{}
	r := newTestReconciler(store, clock)
	r.Step(context.Background()) // initial (empty) rescan
	for i := 0; i < 5; i++ {
		r.Notify("a")
	}
	r.Notify("b")
	r.Step(context.Background())
	if got := store.callCount(); got != 2 {
		t.Fatalf("5 notifies of one name plus one other must reconcile exactly twice, got %d (%v)", got, store.calls)
	}
	if r.Pending() != 0 {
		t.Fatalf("queue must be empty after success, got %d", r.Pending())
	}
}

func TestNotifyDuringReconcileReenqueues(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{}
	r := newTestReconciler(store, clock)
	r.Step(context.Background())
	notified := false
	store.onReconcile = func(name string) {
		if !notified {
			notified = true
			r.Notify(name) // event lands while the derive may predate it
		}
	}
	r.Notify("a")
	r.Step(context.Background())
	if got := store.callCount(); got != 1 {
		t.Fatalf("the re-enqueue must wait for the next step, got %d calls", got)
	}
	if r.Pending() != 1 {
		t.Fatalf("a notify during a reconcile must re-enqueue the name, pending=%d", r.Pending())
	}
	r.Step(context.Background())
	if got := store.callCount(); got != 2 || r.Pending() != 0 {
		t.Fatalf("expected a second reconcile and an empty queue, calls=%d pending=%d", got, r.Pending())
	}
}

func TestReconcilerRetriesThenGivesUp(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{err: errors.New("cas conflict")}
	r := newTestReconciler(store, clock)
	r.BackoffBase, r.BackoffMax, r.MaxRetries = 100*time.Millisecond, 500*time.Millisecond, 4
	var reports []bool
	r.OnError = func(name string, err error, retrying bool) { reports = append(reports, retrying) }
	r.Step(context.Background())
	r.Notify("a")
	start := clock.Now()

	// A tight loop of steps with no clock movement must not retry.
	for i := 0; i < 10; i++ {
		r.Step(context.Background())
	}
	if got := store.callCount(); got != 1 {
		t.Fatalf("a failed name must wait out its backoff, not loop hot: %d calls", got)
	}
	for i := 0; i < 20; i++ {
		clock.Advance(100 * time.Millisecond)
		r.Step(context.Background())
	}
	// 1 initial + MaxRetries retries, spaced 100, 200, 400, 500(cap).
	if got := store.callCount(); got != 5 {
		t.Fatalf("expected 1 attempt + 4 retries then give up, got %d", got)
	}
	var offsets []time.Duration
	for _, at := range store.callTimes {
		offsets = append(offsets, at.Sub(start))
	}
	want := []time.Duration{0, 100 * time.Millisecond, 300 * time.Millisecond, 700 * time.Millisecond, 1200 * time.Millisecond}
	for i := range want {
		if offsets[i] != want[i] {
			t.Fatalf("attempt %d at %v, want %v (offsets %v)", i, offsets[i], want[i], offsets)
		}
	}
	if r.Pending() != 0 {
		t.Fatalf("a given-up name must leave the queue, pending=%d", r.Pending())
	}
	if len(reports) != 5 || !reports[3] || reports[4] {
		t.Fatalf("OnError must report retrying for the first 4 failures and then give up, got %v", reports)
	}
}

func TestReconcilerNotFoundIsDropped(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{err: fmt.Errorf("gone")}
	r := newTestReconciler(store, clock)
	r.IsNotFound = func(err error) bool { return err.Error() == "gone" }
	r.Step(context.Background())
	r.Notify("a")
	r.Step(context.Background())
	clock.Advance(time.Minute)
	if r.Pending() != 0 {
		t.Fatalf("a vanished resource must not be retried, pending=%d", r.Pending())
	}
}

func TestReconcilerNotFoundKeepsConcurrentNotify(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{err: fmt.Errorf("gone")}
	r := newTestReconciler(store, clock)
	r.IsNotFound = func(err error) bool { return err.Error() == "gone" }
	r.Step(context.Background())
	store.onReconcile = func(name string) {
		store.onReconcile = nil
		r.Notify(name) // e.g. the resource is created while the read misses it
	}
	r.Notify("a")
	r.Step(context.Background())
	if r.Pending() != 1 {
		t.Fatalf("a notify that lands during a not-found reconcile must re-enqueue, pending=%d", r.Pending())
	}
}

func TestReconcilerRescanFindsMissedEvent(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{names: []string{"a"}}
	r := newTestReconciler(store, clock)
	r.RescanInterval = time.Minute

	r.Step(context.Background()) // startup rescan recomputes everything
	if got := store.callCount(); got != 1 {
		t.Fatalf("the startup rescan must reconcile existing resources, got %d", got)
	}
	// A resource appears (or its inputs change) with no Notify at all.
	store.mu.Lock()
	store.names = append(store.names, "b")
	store.mu.Unlock()
	clock.Advance(59 * time.Second)
	r.Step(context.Background())
	if got := store.callCount(); got != 1 {
		t.Fatalf("no rescan before RescanInterval, got %d calls", got)
	}
	clock.Advance(2 * time.Second)
	r.Step(context.Background())
	if got := store.callCount(); got != 3 {
		t.Fatalf("the periodic rescan must reconcile a and the missed b, got %d calls (%v)", got, store.calls)
	}
}

func TestReconcilerRescanPagedAndBounded(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{}
	for i := 0; i < 250; i++ {
		store.names = append(store.names, fmt.Sprintf("r%03d", i))
	}
	r := newTestReconciler(store, clock)
	r.MaxBatch = 100
	r.MaxQueue = 1000

	perStep := []int{}
	for i := 0; i < 3; i++ {
		before := store.callCount()
		r.Step(context.Background())
		perStep = append(perStep, store.callCount()-before)
	}
	for i, n := range perStep {
		if n > 100 {
			t.Fatalf("step %d reconciled %d names, MaxBatch is 100", i, n)
		}
	}
	for _, size := range store.listSizes {
		if size > 100 {
			t.Fatalf("a rescan page listed %d names, MaxBatch is 100", size)
		}
	}
	if store.callCount() != 250 || store.listCalls != 3 {
		t.Fatalf("250 resources need 3 pages and 250 reconciles, got %d pages and %d reconciles", store.listCalls, store.callCount())
	}
	seen := map[string]bool{}
	for _, n := range store.calls {
		seen[n] = true
	}
	if len(seen) != 250 {
		t.Fatalf("every resource must be reconciled exactly once per rescan, got %d distinct", len(seen))
	}
}

func TestReconcilerQueueBounded(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{}
	r := newTestReconciler(store, clock)
	r.MaxQueue = 2
	r.Step(context.Background())
	for _, n := range []string{"a", "b", "c", "d", "a"} {
		r.Notify(n)
	}
	if r.Pending() != 2 || r.Dropped() != 2 {
		t.Fatalf("queue must stay at its bound with overflow counted, pending=%d dropped=%d", r.Pending(), r.Dropped())
	}
}

func TestReconcilerNotifyAllIsCoalescedAndSpaced(t *testing.T) {
	clock := newFakeClock()
	store := &fakeStore{names: []string{"a"}}
	r := newTestReconciler(store, clock)
	r.RescanInterval = time.Hour
	r.Step(context.Background()) // startup rescan
	for i := 0; i < 10; i++ {
		r.NotifyAll()
	}
	r.Step(context.Background())
	if store.listCalls != 1 {
		t.Fatalf("an event-triggered rescan must wait EventRescanGap, got %d listings", store.listCalls)
	}
	clock.Advance(time.Second)
	r.Step(context.Background())
	r.Step(context.Background())
	if store.listCalls != 2 {
		t.Fatalf("ten NotifyAll calls must coalesce into one rescan, got %d listings", store.listCalls)
	}
}

func TestReconcilerRunStopsOnCancel(t *testing.T) {
	store := &fakeStore{clock: newFakeClock()}
	r := &Reconciler{Store: store, Clock: store.clock}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	for i := 0; i < 100 && store.listCount() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	r.Notify("a")
	for i := 0; i < 200 && store.callCount() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if store.callCount() == 0 {
		t.Fatal("Run must process a notified name")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return promptly when its context is cancelled")
	}
}

// epochStore models the storage CAS: when the epoch advances between the
// read and the commit, the write is recomputed from current inputs instead of
// committing the older derivation.
type epochStore struct {
	fakeStore
	epoch        int64
	advanceOnce  bool
	storedStatus Status
	storedEpoch  int64
}

func (s *epochStore) ReconcileFleetResource(_ context.Context, name string, derive func(Input) Status, _ func()) (*Resource, error) {
	for attempt := 0; attempt < 3; attempt++ {
		readEpoch := s.epoch
		status := derive(Input{AuthorityEpoch: readEpoch, Now: s.clock.Now()})
		if s.advanceOnce { // another controller took over while we derived
			s.advanceOnce = false
			s.epoch++
		}
		if s.epoch != readEpoch {
			continue // CAS compare failed: recompute
		}
		s.storedStatus, s.storedEpoch = status, readEpoch
		return &Resource{Name: name, Status: status, AuthorityEpoch: readEpoch}, nil
	}
	return nil, errors.New("cas conflict")
}

func TestReconcilerOldControllerCannotOverwriteNewerEpochStatus(t *testing.T) {
	clock := newFakeClock()
	store := &epochStore{epoch: 1, advanceOnce: true}
	store.clock = clock
	r := &Reconciler{Store: store, Clock: clock, Derive: func(in Input) Status {
		return Status{ObservedGeneration: in.AuthorityEpoch, EvaluatedAt: in.Now}
	}}
	r.Step(context.Background())
	r.Notify("a")
	r.Step(context.Background())
	if store.storedEpoch != 2 || store.storedStatus.ObservedGeneration != 2 {
		t.Fatalf("a controller that resumed after the epoch advanced must commit only a status derived under the new epoch, stored epoch=%d derived under=%d",
			store.storedEpoch, store.storedStatus.ObservedGeneration)
	}
}
