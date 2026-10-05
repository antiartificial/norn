package fleettest

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
)

// ReconcilerHarness extends ResourceHarness with what the reconciler
// conformance needs from one backend (docs/v3/fleet-controller/plan.md §3,
// WP12).
type ReconcilerHarness interface {
	ResourceHarness
	// ReconcilerStore is the backend's storage as the reconciler sees it.
	ReconcilerStore() controller.ReconcilerStore
	// IsNotFound reports the backend's ErrFleetResourceNotFound.
	IsNotFound(err error) bool
	// SucceedHolder completes SeedHolderBinding's attempt at commitSHA and
	// frees its fence with a "succeeded" release, as the real path does.
	SucceedHolder(ctx context.Context, commitSHA string) error
}

// stepClock is a manually advanced controller.Clock; After never fires
// because conformance drives Reconciler.Step directly.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *stepClock) After(time.Duration) <-chan time.Time { return nil }
func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// scopedStore limits the reconciler's rescan to the names a case created, so
// resources other cases left in a shared database never affect it.
type scopedStore struct {
	controller.ReconcilerStore
	names []string
}

func (s scopedStore) ListFleetResourceNames(ctx context.Context, after string, limit int) ([]string, error) {
	all, err := s.ReconcilerStore.ListFleetResourceNames(ctx, after, 1<<20)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, name := range all {
		for _, mine := range s.names {
			if name == mine && len(out) < limit {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

// hookStore runs hook once, inside the first reconcile, after every input
// was read and before derive (the afterRead seam).
type hookStore struct {
	controller.ReconcilerStore
	once sync.Once
	hook func()
}

func (s *hookStore) ReconcileFleetResource(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	return s.ReconcilerStore.ReconcileFleetResource(ctx, name, derive, func() { s.once.Do(s.hook) })
}

type reconcilerRig struct {
	clock *stepClock
	// skew shifts the Now derive sees, simulating elapsed time without
	// sleeping (storage stamps real time).
	skew   atomic.Int64
	inputs atomic.Value // last controller.Input derived
}

func newRig() *reconcilerRig {
	return &reconcilerRig{clock: &stepClock{now: time.Now()}}
}

func (g *reconcilerRig) reconciler(h ReconcilerHarness, store controller.ReconcilerStore, names ...string) *controller.Reconciler {
	return &controller.Reconciler{
		Store: scopedStore{ReconcilerStore: store, names: names}, Clock: g.clock, IsNotFound: h.IsNotFound,
		Derive: func(in controller.Input) controller.Status {
			g.inputs.Store(in)
			in.Now = in.Now.Add(time.Duration(g.skew.Load()))
			return controller.DeriveStatus(in)
		},
	}
}

func (g *reconcilerRig) lastInput() controller.Input {
	in, _ := g.inputs.Load().(controller.Input)
	return in
}

// settle steps every reconciler, advancing the fake clock past backoffs,
// until all queues drain.
func (g *reconcilerRig) settle(t *testing.T, rs ...*controller.Reconciler) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		pending := 0
		for _, r := range rs {
			r.Step(ctx)
			pending += r.Pending()
		}
		if pending == 0 {
			return
		}
		g.clock.Advance(time.Second)
	}
	t.Fatal("reconciler queue did not drain")
}

func conditionOf(t *testing.T, r *controller.Resource, conditionType string) controller.Condition {
	t.Helper()
	for _, c := range r.Status.Conditions {
		if c.Type == conditionType {
			return c
		}
	}
	t.Fatalf("status has no %s condition: %+v", conditionType, r.Status.Conditions)
	return controller.Condition{}
}

// normalizedStatus drops the fields that legitimately differ between two
// evaluations of identical inputs (wall-clock stamps).
func normalizedStatus(t *testing.T, r *controller.Resource) string {
	t.Helper()
	status := r.Status
	status.EvaluatedAt = time.Time{}
	status.Conditions = append([]controller.Condition(nil), status.Conditions...)
	for i := range status.Conditions {
		if status.Conditions[i].Type == controller.ConditionReconciliationRequired {
			status.Conditions[i].ObservedAt = time.Time{}
		}
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func commitDesired(commit string) controller.DesiredRevision {
	d := validDesired()
	d.CommitSHA = commit
	return d
}

func seedObservations(t *testing.T, h ReconcilerHarness, name, targetID string, drift bool) {
	t.Helper()
	ctx := context.Background()
	for _, o := range []struct {
		source string
		facts  controller.ObservationFacts
	}{
		{controller.SourceProvider, controller.ObservationFacts{"targetId": targetID, "enrolledNodes": 3.0, "expectedNodes": 3.0}},
		{controller.SourceState, controller.ObservationFacts{"drift": drift}},
		{controller.SourceRuntime, controller.ObservationFacts{"ready": true, "ingressReady": true}},
	} {
		if _, _, err := h.AppendObservation(ctx, name, o.source, time.Now().UTC(), o.facts, nil, "conformance"); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	commitX = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitY = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// RunFleetReconcilerConformance runs the WP12 reconciler cases against h:
// the worker, driven through Step with a fake clock, over the backend's real
// reconcile CAS.
func RunFleetReconcilerConformance(t *testing.T, h ReconcilerHarness) {
	ctx := context.Background()
	store := h.ReconcilerStore()

	newNamed := func(t *testing.T) (string, string) {
		t.Helper()
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		resource, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return name, resource.TargetID
	}
	get := func(t *testing.T, name string) *controller.Resource {
		t.Helper()
		r, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	t.Run("SuccessfulDeploymentAdvancesLastApplied", func(t *testing.T) {
		name, _ := newNamed(t)
		rig := newRig()
		r := rig.reconciler(h, store, name)
		if _, err := h.SetDesired(ctx, name, commitDesired(commitX)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.SeedHolderBinding(ctx, name); err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		running := get(t, name)
		if running.Status.Active == nil || running.Status.Active.Occupancy != "Active" || running.Status.NextAction != controller.NextActionAwaitRunner {
			t.Fatalf("an execution in progress must read Active/await_runner, got %+v", running.Status)
		}
		if running.Status.Active.DispatchRunID != "4242" || len(running.Status.Active.AttemptIDs) != 1 {
			t.Fatalf("storage must hand derive the dispatch run ID and attempts, got %+v", running.Status.Active)
		}
		if rig.lastInput().HolderDispatchRunID != SeedDispatchRunID {
			t.Fatalf("Input.HolderDispatchRunID = %d, want %d", rig.lastInput().HolderDispatchRunID, SeedDispatchRunID)
		}
		if running.Status.LastAppliedGeneration != 0 {
			t.Fatalf("nothing is applied while the run is active, got generation %d", running.Status.LastAppliedGeneration)
		}

		if err := h.SucceedHolder(ctx, commitX); err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		if len(rig.lastInput().LastReleaseAttempts) != 1 {
			t.Fatalf("a succeeded release must hand derive the released plan's attempts, got %d", len(rig.lastInput().LastReleaseAttempts))
		}
		done := get(t, name)
		if done.Status.Active != nil {
			t.Fatalf("a released fence has no active execution, got %+v", done.Status.Active)
		}
		if done.Status.LastAppliedGeneration != 1 || done.Status.LastApplied == nil || done.Status.LastApplied.CommitSHA != commitX {
			t.Fatalf("a succeeded deployment must advance lastApplied to generation 1 at %s, got %d %+v", commitX, done.Status.LastAppliedGeneration, done.Status.LastApplied)
		}

		if _, err := h.SetDesired(ctx, name, commitDesired(commitY)); err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		next := get(t, name)
		rr := conditionOf(t, next, controller.ConditionReconciliationRequired)
		if next.Status.ObservedGeneration != 2 || next.Status.LastAppliedGeneration != 1 || rr.Reason != controller.ReasonDesiredNotApplied {
			t.Fatalf("a newer desired revision must stay unapplied, got observed=%d applied=%d %s", next.Status.ObservedGeneration, next.Status.LastAppliedGeneration, rr.Reason)
		}
	})

	t.Run("DriftIsReportedAndCleared", func(t *testing.T) {
		name, targetID := newNamed(t)
		rig := newRig()
		r := rig.reconciler(h, store, name)
		seedObservations(t, h, name, targetID, true)
		r.Notify(name)
		rig.settle(t, r)
		drifted := get(t, name)
		if c := conditionOf(t, drifted, controller.ConditionDriftDetected); c.Status != controller.StatusTrue {
			t.Fatalf("DriftDetected = %s/%s, want True", c.Status, c.Reason)
		}
		if drifted.Status.NextAction != controller.NextActionInvestigateDrift {
			t.Fatalf("NextAction = %s, want investigate_drift: %+v", drifted.Status.NextAction, drifted.Status.Conditions)
		}
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceState, time.Now().UTC(), controller.ObservationFacts{"drift": false}, nil, "conformance"); err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		clean := get(t, name)
		if c := conditionOf(t, clean, controller.ConditionDriftDetected); c.Status != controller.StatusFalse || clean.Status.NextAction != controller.NextActionNone {
			t.Fatalf("a newer no-drift reading must clear drift, got %s next=%s", c.Status, clean.Status.NextAction)
		}
	})

	t.Run("StaleObservationsGoUnknownAfterRescan", func(t *testing.T) {
		name, targetID := newNamed(t)
		rig := newRig()
		r := rig.reconciler(h, store, name)
		seedObservations(t, h, name, targetID, false)
		r.Notify(name)
		rig.settle(t, r)
		if c := conditionOf(t, get(t, name), controller.ConditionRuntimeReady); c.Status != controller.StatusTrue {
			t.Fatalf("fresh runtime observation must read True, got %s/%s", c.Status, c.Reason)
		}
		// No event arrives; only time passes. The periodic rescan alone must
		// re-derive and downgrade.
		rig.skew.Store(int64(controller.ObservationFreshness + time.Minute))
		rig.clock.Advance(controller.DefaultRescanInterval + time.Second)
		rig.settle(t, r)
		stale := get(t, name)
		for _, ct := range []string{controller.ConditionRuntimeReady, controller.ConditionDriftDetected, controller.ConditionProviderStateKnown} {
			if c := conditionOf(t, stale, ct); c.Status != controller.StatusUnknown || c.Reason != controller.ReasonObservationStale {
				t.Fatalf("%s = %s/%s after the rescan, want Unknown/ObservationStale", ct, c.Status, c.Reason)
			}
		}
		if stale.Status.NextAction != controller.NextActionRefreshObservations {
			t.Fatalf("NextAction = %s, want refresh_observations", stale.Status.NextAction)
		}
	})

	t.Run("InterruptedExecutionShowsUncertain", func(t *testing.T) {
		name, _ := newNamed(t)
		rig := newRig()
		r := rig.reconciler(h, store, name)
		sealed, err := h.SeedHolderBinding(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Authority moves while a run is in flight: the outcome is unknown
		// and nothing is resolved automatically.
		if _, err := h.AdvanceEpoch(ctx, epoch, "conformance-reconciler-interrupted"); err != nil {
			t.Fatal(err)
		}
		r.Notify(name)
		rig.settle(t, r)
		got := get(t, name)
		rr := conditionOf(t, got, controller.ConditionReconciliationRequired)
		if got.Status.Active == nil || got.Status.Active.Occupancy != "Uncertain" || rr.Reason != controller.ReasonOutcomeUncertain || got.Status.NextAction != controller.NextActionResolveUncertainOutcome {
			t.Fatalf("an interrupted execution must read Uncertain/OutcomeUncertain/resolve_uncertain_outcome, got %+v %s", got.Status, rr.Reason)
		}
		after, err := h.SnapshotHolderBinding(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if after != sealed {
			t.Fatalf("the reconciler must never touch fence, dispatch or attempt rows, got\nbefore=%s\nafter =%s", sealed, after)
		}
	})

	t.Run("RestartedReconcilerRecomputes", func(t *testing.T) {
		name, targetID := newNamed(t)
		rig := newRig()
		if _, err := h.SetDesired(ctx, name, commitDesired(commitX)); err != nil {
			t.Fatal(err)
		}
		if _, err := h.SeedHolderBinding(ctx, name); err != nil {
			t.Fatal(err)
		}
		seedObservations(t, h, name, targetID, false)
		first := rig.reconciler(h, store, name)
		first.Notify(name)
		rig.settle(t, first)
		before := normalizedStatus(t, get(t, name))

		// A brand-new controller (no queue, no memory) recomputes the same
		// status from storage alone at its startup rescan.
		restarted := rig.reconciler(h, store, name)
		rig.settle(t, restarted)
		after := get(t, name)
		if normalizedStatus(t, after) != before {
			t.Fatalf("a restarted controller must derive the same status:\nbefore=%s\nafter =%s", before, normalizedStatus(t, after))
		}
	})

	t.Run("RescanOfUnchangedResourceWritesNothing", func(t *testing.T) {
		name, targetID := newNamed(t)
		rig := newRig()
		if _, err := h.SeedHolderBinding(ctx, name); err != nil {
			t.Fatal(err)
		}
		seedObservations(t, h, name, targetID, false)
		r := rig.reconciler(h, store, name)
		r.Notify(name)
		rig.settle(t, r)
		before := get(t, name)
		// Several periodic rescans with nothing changed: no revision bump, so
		// an operator's expected-revision CAS is never broken by the rescan.
		for i := 0; i < 3; i++ {
			rig.clock.Advance(controller.DefaultRescanInterval + time.Second)
			rig.settle(t, r)
		}
		if after := get(t, name); after.Revision != before.Revision {
			t.Fatalf("a rescan of an unchanged resource must not write, revision %d -> %d", before.Revision, after.Revision)
		}
	})

	t.Run("TwoReconcilersConverge", func(t *testing.T) {
		name, targetID := newNamed(t)
		rig := newRig()
		seedObservations(t, h, name, targetID, false)
		a, b := rig.reconciler(h, store, name), rig.reconciler(h, store, name)

		stop := make(chan struct{})
		var workers sync.WaitGroup
		for _, r := range []*controller.Reconciler{a, b} {
			workers.Add(1)
			go func(r *controller.Reconciler) {
				defer workers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					r.Step(ctx)
					rig.clock.Advance(150 * time.Millisecond)
					time.Sleep(time.Millisecond)
				}
			}(r)
		}
		// Strictly after the seeded runtime watermark (now), within the +1m bound.
		base := time.Now().UTC().Add(5 * time.Second)
		const appends = 12
		for i := 0; i < appends; i++ {
			ready := i%2 == 0
			if i == appends-1 {
				ready = false // the newest observation is a failure
			}
			if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, base.Add(time.Duration(i)*time.Millisecond),
				controller.ObservationFacts{"ready": ready, "ingressReady": true}, nil, "conformance"); err != nil {
				t.Fatal(err)
			}
			a.Notify(name)
			b.Notify(name)
		}
		close(stop)
		workers.Wait()
		rig.settle(t, a, b)

		final := get(t, name)
		if c := conditionOf(t, final, controller.ConditionRuntimeReady); c.Status != controller.StatusFalse || c.ObservationSequence != final.ObservationSequence {
			t.Fatalf("two concurrent reconcilers must converge on the newest observation (seq %d), got RuntimeReady=%s seq=%d",
				final.ObservationSequence, c.Status, c.ObservationSequence)
		}
		// One more reconcile from either must be a no-op in content.
		before := normalizedStatus(t, final)
		a.Notify(name)
		rig.settle(t, a)
		if after := normalizedStatus(t, get(t, name)); after != before {
			t.Fatalf("converged status must be stable:\n%s\n%s", before, after)
		}
	})

	t.Run("EpochAdvanceReflectedInStatus", func(t *testing.T) {
		name, _ := newNamed(t)
		rig := newRig()
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.SeedHolderBinding(ctx, name); err != nil {
			t.Fatal(err)
		}
		// Controller A is mid-reconcile (inputs read, derive not yet run)
		// when authority advances and controller B, which has no memory of
		// A, reconciles under the new epoch. A has lost authority: on a
		// backend that retries, its pending write must be refused and
		// recomputed; on one that serializes, A commits first and B's write
		// supersedes it.
		next := make(chan int64, 1)
		competitorErr := make(chan error, 1)
		competitor := rig.reconciler(h, store, name)
		slow := &hookStore{ReconcilerStore: store, hook: func() {
			go func() {
				n, err := h.AdvanceEpoch(ctx, epoch, "conformance-reconciler-old-controller")
				next <- n
				if err == nil {
					_, err = store.ReconcileFleetResource(ctx, name, competitor.Derive, nil)
				}
				competitorErr <- err
			}()
			time.Sleep(50 * time.Millisecond)
		}}
		old := rig.reconciler(h, slow, name)
		old.Notify(name)
		rig.settle(t, old)
		advanced := <-next
		if err := <-competitorErr; err != nil {
			t.Fatal(err)
		}
		resumed := get(t, name)
		if resumed.AuthorityEpoch != rig.lastInput().AuthorityEpoch && !h.Serializes() {
			t.Fatalf("the epoch stamped (%d) must be the epoch derive computed under (%d)", resumed.AuthorityEpoch, rig.lastInput().AuthorityEpoch)
		}
		if !h.Serializes() && resumed.AuthorityEpoch != advanced {
			t.Fatalf("a controller resuming after the epoch advanced must recompute under epoch %d, got %d", advanced, resumed.AuthorityEpoch)
		}

		// Whichever controller runs next, the status ends reflecting the new
		// epoch: the old controller's earlier derivation never survives.
		fresh := rig.reconciler(h, store, name)
		fresh.Notify(name)
		old.Notify(name)
		rig.settle(t, fresh, old)
		final := get(t, name)
		if final.AuthorityEpoch != advanced {
			t.Fatalf("status must be stamped with the advanced epoch %d, got %d", advanced, final.AuthorityEpoch)
		}
		if final.Status.Active == nil || final.Status.Active.OccupancyReason != "AuthoritySuperseded" {
			t.Fatalf("status must reflect the superseded authority, got %+v", final.Status.Active)
		}
	})
}
