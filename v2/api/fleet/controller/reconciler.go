package controller

// The Fleet resource reconciler (docs/v3/fleet-controller/plan.md §2.4,
// WP12). It observes continuously and executes nothing: its only write is
// Store.ReconcileFleetResource, whose CAS (PG row locks; etcd ModRevision
// compares over the resource, authority epoch, fence, holder and watermark
// keys) rejects or recomputes any status derived from stale inputs. An old
// controller that resumes after the authority epoch advanced therefore
// cannot overwrite a newer status: its write is recomputed from current
// inputs and stamped with the current epoch, or refused.
//
// Work arrives two ways, both feeding one bounded, deduplicating in-process
// queue: events (Notify, NotifyAll) after a commit, and a paged periodic
// rescan of every resource that catches missed events and ages out stale
// observations (conditions are frozen at EvaluatedAt, so without the rescan
// a crashed or idle controller would leave a True readiness True forever).
//
// The queue is driven by Step, a deterministic unit with no goroutines or
// timers of its own (tests drive it with a fake Clock); Run is the thin
// loop that sleeps between Steps and exits on context cancellation.

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Reconciler defaults (plan.md §2.4: RescanInterval=60s, MaxBatch=100).
const (
	DefaultRescanInterval = 60 * time.Second
	DefaultMaxBatch       = 100
	DefaultMaxQueue       = 1024
	DefaultBackoffBase    = 100 * time.Millisecond
	DefaultBackoffMax     = 30 * time.Second
	// DefaultMaxRetries is how many times one name is retried after
	// consecutive failures before the reconciler gives up on it until the
	// next event or rescan.
	DefaultMaxRetries = 5
	// DefaultEventRescanGap is the minimum spacing between event-triggered
	// early rescans (NotifyAll), so an event burst cannot make the
	// reconciler rescan hot.
	DefaultEventRescanGap = time.Second
)

// StatusEquivalent reports whether two statuses differ only in their
// evaluation stamps: EvaluatedAt and the ReconciliationRequired condition's
// ObservedAt, both of which DeriveStatus sets to in.Now on every run. Storage
// skips the status write when the stored status is equivalent to a fresh
// derivation under the same authority epoch, so a periodic rescan of an
// unchanged resource writes nothing: no revision bump (which would make an
// operator's expected-revision CAS conflict every RescanInterval), no etcd
// history, no PG dead tuple. Skipping is safe because readers re-apply
// freshness against their own clock (DowngradeStale, M9) and
// ReconciliationRequired's ObservedAt is never read for staleness.
func StatusEquivalent(a, b Status) bool {
	ea, errA := json.Marshal(withoutEvaluationStamps(a))
	eb, errB := json.Marshal(withoutEvaluationStamps(b))
	return errA == nil && errB == nil && bytes.Equal(ea, eb)
}

func withoutEvaluationStamps(s Status) Status {
	s.EvaluatedAt = time.Time{}
	conditions := make([]Condition, len(s.Conditions))
	for i, c := range s.Conditions {
		c.ObservedAt = c.ObservedAt.UTC()
		if c.Type == ConditionReconciliationRequired {
			c.ObservedAt = time.Time{}
		}
		conditions[i] = c
	}
	if s.Conditions == nil {
		conditions = nil
	}
	s.Conditions = conditions
	return s
}

// ReconcilerStore is the slice of each backend's storage the reconciler
// uses: the status CAS and the paged resource listing.
type ReconcilerStore interface {
	ReconcileFleetResource(ctx context.Context, name string, derive func(Input) Status, afterRead func()) (*Resource, error)
	// ListFleetResourceNames returns up to limit names strictly after
	// `after`, in name order.
	ListFleetResourceNames(ctx context.Context, after string, limit int) ([]string, error)
}

// Clock is the reconciler's time source, replaceable in tests.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Reconciler derives and writes Fleet resource status. Zero values for the
// tuning fields select the Default* constants.
type Reconciler struct {
	Store ReconcilerStore
	Clock Clock
	// Derive defaults to DeriveStatus.
	Derive func(Input) Status
	// IsNotFound reports a store error meaning the resource no longer
	// exists; such a name is dropped, not retried.
	IsNotFound func(error) bool
	// OnError, when set, is told about each failed reconcile or rescan
	// listing. name is "" for a listing failure.
	OnError func(name string, err error, retrying bool)

	RescanInterval time.Duration
	MaxBatch       int
	MaxQueue       int
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	MaxRetries     int
	EventRescanGap time.Duration

	mu      sync.Mutex
	items   map[string]*reconcileItem
	wake    chan struct{}
	started bool
	dropped int64

	// rescanDue is when the next rescan page runs; the zero value means
	// "immediately", so a (re)started controller recomputes everything.
	rescanDue     time.Time
	rescanCursor  string
	rescanning    bool
	rescanAgain   bool
	lastRescanEnd time.Time
}

type reconcileItem struct {
	due      time.Time
	failures int
	inflight bool
	// dirty records a notification that arrived while the name was being
	// reconciled: the in-flight derive may predate the event, so the name is
	// re-enqueued when the reconcile ends (m12).
	dirty bool
}

func (r *Reconciler) init() {
	if r.started {
		return
	}
	r.started = true
	r.items = map[string]*reconcileItem{}
	r.wake = make(chan struct{}, 1)
	if r.Clock == nil {
		r.Clock = realClock{}
	}
	if r.Derive == nil {
		r.Derive = DeriveStatus
	}
	if r.RescanInterval <= 0 {
		r.RescanInterval = DefaultRescanInterval
	}
	if r.MaxBatch <= 0 {
		r.MaxBatch = DefaultMaxBatch
	}
	if r.MaxQueue <= 0 {
		r.MaxQueue = DefaultMaxQueue
	}
	if r.BackoffBase <= 0 {
		r.BackoffBase = DefaultBackoffBase
	}
	if r.BackoffMax <= 0 {
		r.BackoffMax = DefaultBackoffMax
	}
	if r.MaxRetries <= 0 {
		r.MaxRetries = DefaultMaxRetries
	}
	if r.EventRescanGap <= 0 {
		r.EventRescanGap = DefaultEventRescanGap
	}
}

func (r *Reconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Notify enqueues name after an event that may have changed its inputs.
// Names already queued coalesce; a name being reconciled right now is
// re-enqueued when that reconcile ends.
func (r *Reconciler) Notify(name string) {
	if name == "" {
		return
	}
	r.mu.Lock()
	r.init()
	r.enqueueLocked(name)
	r.mu.Unlock()
	r.signal()
}

// NotifyAll requests an early, coalesced rescan of every resource. It serves
// events (plans, dispatches, checkpoints, attempts, fence changes) that do not
// carry a resource name; the queue stays bounded because the rescan is paged
// and at most one runs per EventRescanGap.
func (r *Reconciler) NotifyAll() {
	r.mu.Lock()
	r.init()
	if r.rescanning {
		// Names already paged past may have been missed: scan again once
		// this pass completes.
		r.rescanAgain = true
	} else {
		due := r.Clock.Now()
		if earliest := r.lastRescanEnd.Add(r.EventRescanGap); due.Before(earliest) {
			due = earliest
		}
		if r.rescanDue.IsZero() || due.Before(r.rescanDue) {
			r.rescanDue = due
		}
	}
	r.mu.Unlock()
	r.signal()
}

// Dropped reports how many enqueues were refused because the queue was full.
// A dropped name is still reached by the next rescan.
func (r *Reconciler) Dropped() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// LastRescan reports when the most recent rescan finished listing every
// resource (the zero time before the first one completes) and the interval
// between rescans. WP13's read API reports it as controller liveness, since
// an unchanged status is no longer rewritten (WP12 review).
func (r *Reconciler) LastRescan() (at time.Time, interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	interval = r.RescanInterval
	if interval <= 0 {
		interval = DefaultRescanInterval
	}
	return r.lastRescanEnd, interval
}

// Pending reports the number of queued or in-flight names.
func (r *Reconciler) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

func (r *Reconciler) enqueueLocked(name string) {
	if item, ok := r.items[name]; ok {
		if item.inflight {
			item.dirty = true
		}
		return
	}
	if len(r.items) >= r.MaxQueue {
		r.dropped++
		return
	}
	r.items[name] = &reconcileItem{due: r.Clock.Now()}
}

func (r *Reconciler) backoff(failures int) time.Duration {
	d := r.BackoffBase
	for i := 1; i < failures && d < r.BackoffMax; i++ {
		d *= 2
	}
	if d > r.BackoffMax {
		d = r.BackoffMax
	}
	return d
}

// Step performs one bounded unit of work: at most one rescan page and at
// most MaxBatch reconciles of names that are due. It returns the time of the
// next work (an earlier time than now means more due work remains).
func (r *Reconciler) Step(ctx context.Context) time.Time {
	r.mu.Lock()
	r.init()
	r.mu.Unlock()

	r.rescanPage(ctx)

	r.mu.Lock()
	now := r.Clock.Now()
	due := make([]string, 0, len(r.items))
	for name, item := range r.items {
		if !item.inflight && !item.due.After(now) {
			due = append(due, name)
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := r.items[due[i]], r.items[due[j]]
		if !a.due.Equal(b.due) {
			return a.due.Before(b.due)
		}
		return due[i] < due[j]
	})
	if len(due) > r.MaxBatch {
		due = due[:r.MaxBatch]
	}
	r.mu.Unlock()

	for _, name := range due {
		if ctx.Err() != nil {
			break
		}
		r.reconcileOne(ctx, name)
	}
	return r.nextWake()
}

func (r *Reconciler) nextWake() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.rescanDue
	for _, item := range r.items {
		if !item.inflight && (next.IsZero() || item.due.Before(next)) {
			next = item.due
		}
	}
	return next
}

// rescanPage lists one page of resources when a rescan is due and enqueues
// it. The cursor persists across Steps, so a rescan of N resources costs
// ceil(N/MaxBatch) bounded pages.
func (r *Reconciler) rescanPage(ctx context.Context) {
	r.mu.Lock()
	now := r.Clock.Now()
	if r.rescanDue.After(now) {
		r.mu.Unlock()
		return
	}
	cursor := r.rescanCursor
	if !r.rescanning {
		r.rescanning, r.rescanAgain = true, false
	}
	limit := r.MaxBatch
	r.mu.Unlock()

	names, err := r.Store.ListFleetResourceNames(ctx, cursor, limit)

	r.mu.Lock()
	defer r.mu.Unlock()
	now = r.Clock.Now()
	if err != nil {
		if ctx.Err() == nil && r.OnError != nil {
			r.OnError("", err, true)
		}
		r.rescanDue = now.Add(r.BackoffMax)
		return
	}
	for _, name := range names {
		r.enqueueLocked(name)
	}
	if len(names) == limit {
		r.rescanCursor = names[len(names)-1]
		return // more pages: stay due so the next Step continues
	}
	r.rescanCursor, r.rescanning = "", false
	r.lastRescanEnd = now
	if r.rescanAgain {
		r.rescanDue = now.Add(r.EventRescanGap)
	} else {
		r.rescanDue = now.Add(r.RescanInterval)
	}
	r.rescanAgain = false
}

// reconcileOne runs one CAS reconcile. A CAS conflict (or any other error)
// re-enqueues the name with bounded exponential backoff; after MaxRetries
// consecutive failures the name is dropped until the next event or rescan.
func (r *Reconciler) reconcileOne(ctx context.Context, name string) {
	r.mu.Lock()
	item, ok := r.items[name]
	if !ok || item.inflight || item.due.After(r.Clock.Now()) {
		r.mu.Unlock()
		return
	}
	item.inflight, item.dirty = true, false
	r.mu.Unlock()

	_, err := r.Store.ReconcileFleetResource(ctx, name, r.Derive, nil)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.Clock.Now()
	switch {
	case err == nil:
		item.failures = 0
	case ctx.Err() != nil:
		// Shutting down: not a failure.
		delete(r.items, name)
		return
	case r.IsNotFound != nil && r.IsNotFound(err):
		// Not retried; a notification that arrived meanwhile (e.g. the
		// resource was just created) still re-enqueues below.
		item.failures = 0
	default:
		item.failures++
		retrying := item.failures <= r.MaxRetries
		if r.OnError != nil {
			r.OnError(name, err, retrying)
		}
		if retrying {
			item.inflight, item.dirty = false, false
			item.due = now.Add(r.backoff(item.failures))
			return
		}
		item.failures = 0
	}
	if item.dirty {
		item.inflight, item.dirty = false, false
		item.due = now
		return
	}
	delete(r.items, name)
}

// Run drives Step until ctx is cancelled. It returns promptly on
// cancellation and never spins: between Steps it sleeps until the next due
// work, a notification, or cancellation.
func (r *Reconciler) Run(ctx context.Context) {
	r.mu.Lock()
	r.init()
	r.mu.Unlock()
	for ctx.Err() == nil {
		next := r.Step(ctx)
		d := next.Sub(r.Clock.Now())
		if d <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-r.Clock.After(d):
		}
	}
}
