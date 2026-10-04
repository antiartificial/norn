package fleettest

import (
	"context"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
)

// ResourceHarness lets RunFleetResourceConformance exercise one backend's
// Fleet resource and observation storage through a uniform surface
// (docs/v3/fleet-controller/plan.md §2.3, §3, WP10). Each backend package
// wires its own Harness against its real storage and calls
// RunFleetResourceConformance from a top-level
// TestFleetResourceConformance{Postgres,Etcd} test.
type ResourceHarness interface {
	// NewResource registers a fresh target and an empty resource bound to
	// it, returning the resource's unique name.
	NewResource(ctx context.Context) (name string, err error)

	// SetDesired accepts a new desired revision (Q8).
	SetDesired(ctx context.Context, name string, next controller.DesiredRevision) (*controller.Resource, error)
	// IsDesiredInvalid reports whether err is the backend's
	// ErrFleetResourceDesiredInvalid refusal.
	IsDesiredInvalid(err error) bool
	// GetResource returns name's current resource.
	GetResource(ctx context.Context, name string) (*controller.Resource, error)

	// AppendObservation ingests one observation for name.
	AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error)
	// IsObservationOutOfBounds reports whether err is the backend's
	// ErrFleetObservationOutOfBounds refusal.
	IsObservationOutOfBounds(err error) bool
	// ListObservations returns up to limit observations for name, newest
	// sequence first.
	ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error)

	// Reconcile runs one CAS status write. afterRead, when non-nil, is
	// invoked once, synchronously, after every input is read but before
	// derive is called -- see store.DB.ReconcileFleetResource's doc comment.
	Reconcile(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error)
	// Serializes reports whether this backend serializes a reconcile
	// against a concurrent write (PG) rather than retrying and recomputing
	// from the newest inputs within the same call (etcd).
	Serializes() bool

	// Epoch and AdvanceEpoch read and CAS the singleton authority epoch
	// (shared with the target domain's storage).
	Epoch(ctx context.Context) (int64, error)
	AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error)

	// SeedHolderBinding makes name's own target fence held by a fresh plan
	// with a dispatch binding and one running attempt (an execution in
	// progress on this resource), and returns an opaque snapshot of the
	// fence, dispatch and attempt rows' current byte representation.
	SeedHolderBinding(ctx context.Context, name string) (snapshot string, err error)
	// SnapshotHolderBinding returns the same snapshot shape as
	// SeedHolderBinding, read back later for comparison.
	SnapshotHolderBinding(ctx context.Context) (snapshot string, err error)
	// AddHolderAttempt creates one more attempt for SeedHolderBinding's plan.
	AddHolderAttempt(ctx context.Context) error
}

// validDesired is a well-formed operator-declared desired revision.
func validDesired() controller.DesiredRevision {
	return controller.DesiredRevision{
		CommitSHA: "0123456789abcdef0123456789abcdef01234567", Repository: "acme/norn-fleet",
		Verification: controller.VerificationOperatorDeclared, AcceptedAt: time.Now().UTC(), AcceptedBy: "op",
	}
}

// failureDerive reports "ObservedFailure" when any runtime watermark row
// (the latest or a tie) carries failure=true, the tie rule plan.md §2.3
// gives derive.
func failureDerive(in controller.Input) controller.Status {
	reason := "Fresh"
	rows := append([]controller.Observation{in.LatestObservations[controller.SourceRuntime]}, in.TiedObservations[controller.SourceRuntime]...)
	for _, row := range rows {
		if failure, _ := row.Facts["failure"].(bool); failure {
			reason = "ObservedFailure"
		}
	}
	return controller.Status{EvaluatedAt: in.Now, Conditions: []controller.Condition{{Type: "RuntimeReady", Reason: reason}}}
}

func statusReason(r *controller.Resource) string {
	if r == nil || len(r.Status.Conditions) != 1 {
		return ""
	}
	return r.Status.Conditions[0].Reason
}

// RunFleetResourceConformance runs the WP10 resource/observation
// conformance cases against h.
func RunFleetResourceConformance(t *testing.T, h ResourceHarness) {
	ctx := context.Background()

	t.Run("DesiredBumpsGenerationHistoryBounded", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		const revisions = controller.DesiredHistoryLimit + 5
		for i := 1; i <= revisions; i++ {
			if _, err := h.SetDesired(ctx, name, validDesired()); err != nil {
				t.Fatalf("SetDesired #%d: %v", i, err)
			}
		}
		resource, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if resource.Desired.Generation != int64(revisions) {
			t.Fatalf("desired generation = %d, want %d", resource.Desired.Generation, revisions)
		}
		if len(resource.DesiredHistory) != controller.DesiredHistoryLimit {
			t.Fatalf("desired history length = %d, want %d (bounded)", len(resource.DesiredHistory), controller.DesiredHistoryLimit)
		}
		if resource.DesiredHistory[0].Generation != int64(revisions-1) {
			t.Fatalf("newest history entry's generation = %d, want %d", resource.DesiredHistory[0].Generation, revisions-1)
		}
		oldestKept := int64(revisions - controller.DesiredHistoryLimit)
		if got := resource.DesiredHistory[len(resource.DesiredHistory)-1].Generation; got != oldestKept {
			t.Fatalf("oldest retained history entry's generation = %d, want %d", got, oldestKept)
		}
	})

	t.Run("ObservationSequenceMonotonic", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		_, first, err := h.AppendObservation(ctx, name, controller.SourceProvider, now, controller.ObservationFacts{"n": 1.0}, nil, "r1")
		if err != nil {
			t.Fatal(err)
		}
		_, second, err := h.AppendObservation(ctx, name, controller.SourceState, now.Add(time.Second), controller.ObservationFacts{"n": 2.0}, nil, "r2")
		if err != nil {
			t.Fatal(err)
		}
		if second.Sequence != first.Sequence+1 {
			t.Fatalf("sequence must be strictly monotonic per resource across sources, got %d then %d", first.Sequence, second.Sequence)
		}
	})

	t.Run("OlderObservationStoredNotApplied", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		_, newer, err := h.AppendObservation(ctx, name, controller.SourceProvider, now, controller.ObservationFacts{"state": "ready"}, nil, "r")
		if err != nil {
			t.Fatal(err)
		}
		if !newer.Applied {
			t.Fatal("the first observation for a source must always apply")
		}
		resource, older, err := h.AppendObservation(ctx, name, controller.SourceProvider, now.Add(-time.Minute), controller.ObservationFacts{"state": "stale"}, nil, "r")
		if err != nil {
			t.Fatal(err)
		}
		if older.Applied {
			t.Fatal("an older observation must never be marked applied, or it could clear a newer failure (M10)")
		}
		if wm := resource.Watermarks[controller.SourceProvider]; wm.Sequence != newer.Sequence {
			t.Fatalf("the source's watermark must still point at the newer observation, got sequence %d, want %d", wm.Sequence, newer.Sequence)
		}
		observations, err := h.ListObservations(ctx, name, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(observations) != 2 {
			t.Fatalf("the older observation must still be stored (never rejected), got %d rows", len(observations))
		}
	})

	t.Run("ObservationTimestampBounds", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now.Add(-25*time.Hour), controller.ObservationFacts{}, nil, "r"); err == nil || !h.IsObservationOutOfBounds(err) {
			t.Fatalf("an observation more than 24h in the past must be refused, got %v", err)
		}
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now.Add(2*time.Minute), controller.ObservationFacts{}, nil, "r"); err == nil || !h.IsObservationOutOfBounds(err) {
			t.Fatalf("an observation more than 1m in the future must be refused, got %v", err)
		}
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now, controller.ObservationFacts{}, nil, "r"); err != nil {
			t.Fatalf("an observation within bounds must succeed, got %v", err)
		}
	})

	t.Run("PruneNeverResurrectsOlderObservation", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		_, stateObs, err := h.AppendObservation(ctx, name, controller.SourceState, now, controller.ObservationFacts{"ready": true}, nil, "r")
		if err != nil {
			t.Fatal(err)
		}
		const flood = controller.ObservationLimit + 10
		for i := 0; i < flood; i++ {
			// Anchored strictly in the past and strictly increasing, so the
			// flood never crosses the ReceivedAt+1m future bound no matter
			// how large ObservationLimit is.
			observedAt := now.Add(-time.Duration(flood-i) * time.Second)
			if _, _, err := h.AppendObservation(ctx, name, controller.SourceProvider, observedAt, controller.ObservationFacts{"n": float64(i)}, nil, "r"); err != nil {
				t.Fatalf("flood append #%d: %v", i, err)
			}
		}
		resource, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if wm := resource.Watermarks[controller.SourceState]; wm.Sequence != stateObs.Sequence {
			t.Fatalf("flooding another source must never change this source's watermark, got sequence %d, want %d", wm.Sequence, stateObs.Sequence)
		}
		observations, err := h.ListObservations(ctx, name, controller.ObservationLimit+50)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, obs := range observations {
			if obs.Source == controller.SourceState && obs.Sequence == stateObs.Sequence {
				found = true
			}
		}
		if !found {
			t.Fatal("pruning must never delete the row a watermark references (M10)")
		}
	})

	t.Run("ReconcileWriteSeesLatestInputs", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now, controller.ObservationFacts{"failure": false}, nil, "seed"); err != nil {
			t.Fatal(err)
		}
		derive := failureDerive
		raceDone := make(chan error, 1)
		afterRead := func() {
			go func() {
				_, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, time.Now().UTC(), controller.ObservationFacts{"failure": true}, nil, "race")
				raceDone <- err
			}()
			time.Sleep(50 * time.Millisecond)
		}
		resource, err := h.Reconcile(ctx, name, derive, afterRead)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-raceDone; err != nil {
			t.Fatal(err)
		}
		gotFailure := len(resource.Status.Conditions) == 1 && resource.Status.Conditions[0].Reason == "ObservedFailure"
		if h.Serializes() {
			if gotFailure {
				t.Fatal("a backend that serializes must commit against the pre-race inputs it read under lock, never the race's write")
			}
			later, err := h.Reconcile(ctx, name, derive, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(later.Status.Conditions) != 1 || later.Status.Conditions[0].Reason != "ObservedFailure" {
				t.Fatal("a later reconcile must see the race's now-committed observation")
			}
		} else {
			if !gotFailure {
				t.Fatal("a backend that retries must recompute from the newest inputs within the same call (M9)")
			}
		}
	})

	t.Run("ConcurrentReconcileWritesConverge", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		before, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		derive := func(in controller.Input) controller.Status { return controller.Status{EvaluatedAt: in.Now} }
		const writers = 3
		errs := make(chan error, writers)
		for i := 0; i < writers; i++ {
			go func() {
				_, err := h.Reconcile(ctx, name, derive, nil)
				errs <- err
			}()
		}
		for i := 0; i < writers; i++ {
			if err := <-errs; err != nil {
				t.Fatalf("concurrent reconcile #%d: %v", i, err)
			}
		}
		after, err := h.GetResource(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if after.Revision != before.Revision+int64(writers) {
			t.Fatalf("every concurrent reconcile must be reflected exactly once with none lost, got revision %d -> %d (want +%d)", before.Revision, after.Revision, writers)
		}
	})

	t.Run("OldEpochWriterRecomputes", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		derive := func(in controller.Input) controller.Status { return controller.Status{EvaluatedAt: in.Now} }
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		first, err := h.Reconcile(ctx, name, derive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first.AuthorityEpoch != epoch {
			t.Fatalf("a reconcile must stamp the current authority epoch, got %d, want %d", first.AuthorityEpoch, epoch)
		}
		next, err := h.AdvanceEpoch(ctx, epoch, "conformance-resource-old-epoch")
		if err != nil {
			t.Fatal(err)
		}
		second, err := h.Reconcile(ctx, name, derive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if second.AuthorityEpoch != next {
			t.Fatalf("a reconcile must always recompute the freshly read epoch, never a stale cached one, got %d, want %d", second.AuthorityEpoch, next)
		}
	})

	t.Run("DesiredChangeDuringExecutionKeepsBindings", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		before, err := h.SeedHolderBinding(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.SetDesired(ctx, name, validDesired()); err != nil {
			t.Fatal(err)
		}
		after, err := h.SnapshotHolderBinding(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("PUT desired must not touch any fence, dispatch or attempt row, got\nbefore=%s\nafter=%s", before, after)
		}
	})
	t.Run("DesiredRevisionValidated", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mergedNoPlan := validDesired()
		mergedNoPlan.Verification = controller.VerificationGitHubMergedPlan
		shortSHA := validDesired()
		shortSHA.CommitSHA = "abc123"
		unknown := validDesired()
		unknown.Verification = "pending-proposal"
		noPrincipal := validDesired()
		noPrincipal.AcceptedBy = ""
		for label, rev := range map[string]controller.DesiredRevision{
			"merged-plan without planId": mergedNoPlan, "short sha": shortSHA, "unknown verification": unknown, "no principal": noPrincipal,
		} {
			if _, err := h.SetDesired(ctx, name, rev); err == nil || !h.IsDesiredInvalid(err) {
				t.Fatalf("%s must be refused as an invalid desired revision, got %v", label, err)
			}
		}
		merged := validDesired()
		merged.Verification, merged.PlanID = controller.VerificationGitHubMergedPlan, "plan-merged"
		resource, err := h.SetDesired(ctx, name, merged)
		if err != nil {
			t.Fatal(err)
		}
		if resource.Desired.Generation != 1 {
			t.Fatalf("refused desired revisions must not bump the generation, got %d", resource.Desired.Generation)
		}
	})

	t.Run("UnknownSourceAndOversizedStringsRefused", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, _, err := h.AppendObservation(ctx, name, "custom", now, nil, nil, "r"); err == nil || !h.IsObservationOutOfBounds(err) {
			t.Fatalf("an unknown source must be refused (it would add an unprunable watermark), got %v", err)
		}
		long := strings.Repeat("x", controller.MaxObservationStringBytes+1)
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now, nil, []string{long}, "r"); err == nil || !h.IsObservationOutOfBounds(err) {
			t.Fatalf("an oversized evidence ref must be refused, got %v", err)
		}
	})

	t.Run("OlderSuccessCannotClearNewerFailure", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now, controller.ObservationFacts{"failure": true}, nil, "r"); err != nil {
			t.Fatal(err)
		}
		_, older, err := h.AppendObservation(ctx, name, controller.SourceRuntime, now.Add(-time.Minute), controller.ObservationFacts{"failure": false}, nil, "r")
		if err != nil {
			t.Fatal(err)
		}
		if older.Applied {
			t.Fatal("an older success must not apply over a newer failure (M10)")
		}
		// A fresh report on another source must not block this one's
		// watermark: watermarks are per source.
		if _, other, err := h.AppendObservation(ctx, name, controller.SourceProvider, now.Add(-2*time.Minute), nil, nil, "r"); err != nil || !other.Applied {
			t.Fatalf("another source's first (older) observation must apply independently, applied=%v err=%v", other.Applied, err)
		}
		resource, err := h.Reconcile(ctx, name, failureDerive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if statusReason(resource) != "ObservedFailure" {
			t.Fatalf("derive must see the newer failure, got %q", statusReason(resource))
		}
	})

	t.Run("EqualTimestampFailureReachesDerive", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		at := time.Now().UTC().Truncate(time.Microsecond)
		if _, _, err := h.AppendObservation(ctx, name, controller.SourceRuntime, at, controller.ObservationFacts{"failure": false}, nil, "r"); err != nil {
			t.Fatal(err)
		}
		_, tie, err := h.AppendObservation(ctx, name, controller.SourceRuntime, at, controller.ObservationFacts{"failure": true}, nil, "r")
		if err != nil {
			t.Fatal(err)
		}
		if !tie.Applied {
			t.Fatal("an observation tied with the watermark must apply so derive can let the failure win")
		}
		for i := 0; i < controller.ObservationLimit+5; i++ {
			if _, _, err := h.AppendObservation(ctx, name, controller.SourceProvider, at.Add(-time.Hour+time.Duration(i)*time.Second), nil, nil, "r"); err != nil {
				t.Fatal(err)
			}
		}
		resource, err := h.Reconcile(ctx, name, failureDerive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if statusReason(resource) != "ObservedFailure" {
			t.Fatalf("on equal ObservedAt the failure must reach derive and win, even after pruning, got %q", statusReason(resource))
		}
	})

	t.Run("ReconcileSeesNewHolderAttempt", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.SeedHolderBinding(ctx, name); err != nil {
			t.Fatal(err)
		}
		derive := func(in controller.Input) controller.Status {
			active := &controller.ActiveRefs{PlanID: in.Fence.HolderPlanID, FenceGeneration: in.Fence.Generation}
			for _, attempt := range in.Holder.Attempts {
				active.AttemptIDs = append(active.AttemptIDs, attempt.ID)
			}
			return controller.Status{EvaluatedAt: in.Now, Active: active}
		}
		raceDone := make(chan error, 1)
		afterRead := func() {
			go func() { raceDone <- h.AddHolderAttempt(ctx) }()
			time.Sleep(50 * time.Millisecond)
		}
		resource, err := h.Reconcile(ctx, name, derive, afterRead)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-raceDone; err != nil {
			t.Fatal(err)
		}
		if resource.Status.Active == nil {
			t.Fatal("a held fence must reach derive")
		}
		if !h.Serializes() && len(resource.Status.Active.AttemptIDs) != 2 {
			t.Fatalf("an attempt created after the read must fail the commit compare and be re-read (M9), got %d attempts", len(resource.Status.Active.AttemptIDs))
		}
		later, err := h.Reconcile(ctx, name, derive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if later.Status.Active == nil || len(later.Status.Active.AttemptIDs) != 2 {
			t.Fatal("a later reconcile must see both attempts")
		}
	})

	t.Run("OldControllerResumesAfterEpochAdvance", func(t *testing.T) {
		name, err := h.NewResource(ctx)
		if err != nil {
			t.Fatal(err)
		}
		epoch, err := h.Epoch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// derive records the epoch it computed under; the committed stamp
		// must be exactly that one, never a fresher epoch over a status
		// computed under an older one.
		derive := func(in controller.Input) controller.Status {
			return controller.Status{EvaluatedAt: in.Now, ObservedGeneration: in.AuthorityEpoch}
		}
		advanced := make(chan int64, 1)
		advanceErr := make(chan error, 1)
		afterRead := func() {
			go func() {
				next, err := h.AdvanceEpoch(ctx, epoch, "conformance-resource-resume")
				advanced <- next
				advanceErr <- err
			}()
			time.Sleep(50 * time.Millisecond)
		}
		resumed, err := h.Reconcile(ctx, name, derive, afterRead)
		if err != nil {
			t.Fatal(err)
		}
		next := <-advanced
		if err := <-advanceErr; err != nil {
			t.Fatal(err)
		}
		if resumed.AuthorityEpoch != resumed.Status.ObservedGeneration {
			t.Fatalf("the epoch stamped (%d) must be the epoch derive computed under (%d)", resumed.AuthorityEpoch, resumed.Status.ObservedGeneration)
		}
		if !h.Serializes() && resumed.AuthorityEpoch != next {
			t.Fatalf("an epoch advance after the read must fail the commit compare and be recomputed, got %d, want %d", resumed.AuthorityEpoch, next)
		}
		final, err := h.Reconcile(ctx, name, derive, nil)
		if err != nil {
			t.Fatal(err)
		}
		if final.AuthorityEpoch != next || final.Status.ObservedGeneration != next {
			t.Fatalf("the next reconcile must compute and stamp the advanced epoch %d, got stamp=%d derived=%d", next, final.AuthorityEpoch, final.Status.ObservedGeneration)
		}
	})
}
