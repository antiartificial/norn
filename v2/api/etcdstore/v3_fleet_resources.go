package etcdstore

// This file is the etcd half of WP10 (docs/v3/fleet-controller/plan.md §2.3,
// §3): the Fleet resource and its observation stream. It mirrors
// store/fleet_resources.go on etcd exactly as etcdstore/v3_fleet_targets.go
// mirrors store/fleet_targets.go: PG locks a fixed row order inside one
// transaction; etcd instead reads everything first, calls the caller's
// derive function, and commits a Txn whose compares cover every key it
// read, retrying from a fresh read (never blindly) on a lost race (T4).
//
// Key layout, relative to s.prefix (plan.md §2.3):
//
//	/v3/fleet-resources/<name>                 JSON controller.Resource
//	/v3/fleet-observations/<name>/<%020d>      JSON controller.Observation,
//	                                            keyed by its zero-padded
//	                                            Sequence for lexical = numeric
//	                                            order (mirrors
//	                                            v3_database_catalog.go's
//	                                            revision keys)
//
// Status is a derived cache (mirroring §2.2's fence "lock, not a ledger"
// rule): it stores no second operation ledger, only IDs that already live
// in the target fence, dispatch-preparation/binding and attempt keys.
//
// Monotonicity (M10) and the CAS obligations (M9) are implemented exactly as
// store/fleet_resources.go's header comment describes, substituting ModRevision
// compares for row locks.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/lifecycle"
)

// ErrFleetResourceNotFound mirrors store.ErrFleetResourceNotFound.
var ErrFleetResourceNotFound = fmt.Errorf("fleet resource not found")

// ErrFleetResourceTargetMismatch mirrors store.ErrFleetResourceTargetMismatch.
var ErrFleetResourceTargetMismatch = fmt.Errorf("fleet resource is already bound to a different target")

// ErrFleetObservationOutOfBounds mirrors store.ErrFleetObservationOutOfBounds.
var ErrFleetObservationOutOfBounds = fmt.Errorf("fleet observation is out of bounds")

// ErrFleetResourceDesiredInvalid mirrors store.ErrFleetResourceDesiredInvalid.
var ErrFleetResourceDesiredInvalid = fmt.Errorf("fleet resource desired revision is invalid")

// ErrFleetResourceReconcileConflict is returned by ReconcileFleetResource
// once every retry has lost its commit race (plan.md §2.3: "retry up to 3
// times, then ErrStatusPreconditionFailed").
var ErrFleetResourceReconcileConflict = fmt.Errorf("fleet resource reconcile lost its commit race after every retry")

const fleetResourceReconcileRetries = 3

func (s *V3OperationStore) fleetResourceKey(name string) string {
	return s.prefix + "/v3/fleet-resources/" + name
}
func (s *V3OperationStore) fleetObservationPrefix(name string) string {
	return s.prefix + "/v3/fleet-observations/" + name + "/"
}
func (s *V3OperationStore) fleetObservationKey(name string, sequence int64) string {
	return fmt.Sprintf("%s%020d", s.fleetObservationPrefix(name), sequence)
}

// EnsureFleetResource idempotently creates name's resource bound to
// targetID. Replaying the same targetID is a no-op; a different targetID is
// refused (ErrFleetResourceTargetMismatch).
func (s *V3OperationStore) EnsureFleetResource(ctx context.Context, name, targetID string) (*controller.Resource, error) {
	if !controller.ValidResourceName(name) || targetID == "" {
		return nil, ErrFleetResourceDesiredInvalid
	}
	response, err := s.kv.Get(ctx, s.fleetResourceKey(name))
	if err != nil {
		return nil, err
	}
	if len(response.Kvs) == 1 {
		var existing controller.Resource
		if err := decodeV3Record(response.Kvs[0].Value, &existing); err != nil {
			return nil, fmt.Errorf("fleet resource is corrupt: %w", err)
		}
		if existing.TargetID != targetID {
			return nil, ErrFleetResourceTargetMismatch
		}
		return &existing, nil
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	resource := controller.Resource{SchemaVersion: controller.SchemaVersion, Name: name, TargetID: targetID, CreatedAt: now, UpdatedAt: now}
	encoded, err := json.Marshal(resource)
	if err != nil {
		return nil, err
	}
	key := s.fleetResourceKey(name)
	// Mirror PG's fleet_resources.target_id FK: the target's fence key must
	// exist (registration creates it and nothing deletes it).
	txn, err := s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(s.fleetTargetFenceKey(targetID)), ">", 0)).
		Then(clientv3.OpPut(key, string(encoded))).
		Commit()
	if err != nil {
		return nil, err
	}
	if !txn.Succeeded {
		fence, err := s.kv.Get(ctx, s.fleetTargetFenceKey(targetID), clientv3.WithCountOnly())
		if err != nil {
			return nil, err
		}
		if fence.Count == 0 {
			return nil, fmt.Errorf("fleet target %q is not registered", targetID)
		}
		// Lost a concurrent create; re-read and apply the same idempotency
		// check rather than retrying blindly.
		return s.EnsureFleetResource(ctx, name, targetID)
	}
	return &resource, nil
}

func (s *V3OperationStore) getFleetResourceWithRevision(ctx context.Context, name string) (controller.Resource, int64, error) {
	response, err := s.kv.Get(ctx, s.fleetResourceKey(name))
	if err != nil {
		return controller.Resource{}, 0, err
	}
	if len(response.Kvs) == 0 {
		return controller.Resource{}, 0, ErrFleetResourceNotFound
	}
	var resource controller.Resource
	if err := decodeV3Record(response.Kvs[0].Value, &resource); err != nil {
		return controller.Resource{}, 0, fmt.Errorf("fleet resource is corrupt: %w", err)
	}
	return resource, response.Kvs[0].ModRevision, nil
}

// GetFleetResource returns name's resource, or ErrFleetResourceNotFound.
func (s *V3OperationStore) GetFleetResource(ctx context.Context, name string) (*controller.Resource, error) {
	resource, _, err := s.getFleetResourceWithRevision(ctx, name)
	if err != nil {
		return nil, err
	}
	return &resource, nil
}

// ListFleetResourceNames returns up to limit resource names strictly after
// `after`, in name order: the reconciler's paged rescan cursor.
func (s *V3OperationStore) ListFleetResourceNames(ctx context.Context, after string, limit int) ([]string, error) {
	prefix := s.fleetResourceKey("")
	start := prefix
	if after != "" {
		start = s.fleetResourceKey(after) + "\x00"
	}
	response, err := s.kv.Get(ctx, start, clientv3.WithRange(clientv3.GetPrefixRangeEnd(prefix)), clientv3.WithKeysOnly(), clientv3.WithLimit(int64(limit)), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(response.Kvs))
	for _, kv := range response.Kvs {
		names = append(names, string(kv.Key)[len(prefix):])
	}
	return names, nil
}

// SetDesiredFleetResource accepts a new desired revision (Q8). It touches
// only this resource's own key: no fence, dispatch-preparation/binding or
// attempt key is read or written (DesiredChangeDuringExecutionKeepsBindings).
func (s *V3OperationStore) SetDesiredFleetResource(ctx context.Context, name string, next controller.DesiredRevision) (*controller.Resource, error) {
	if err := controller.ValidateDesired(next); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFleetResourceDesiredInvalid, err)
	}
	for {
		resource, revision, err := s.getFleetResourceWithRevision(ctx, name)
		if err != nil {
			return nil, err
		}
		if resource.Desired.Generation > 0 {
			resource.DesiredHistory = append([]controller.DesiredRevision{resource.Desired}, resource.DesiredHistory...)
		}
		if len(resource.DesiredHistory) > controller.DesiredHistoryLimit {
			resource.DesiredHistory = resource.DesiredHistory[:controller.DesiredHistoryLimit]
		}
		next.Generation = resource.Desired.Generation + 1
		resource.Desired = next
		resource.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)

		encoded, err := json.Marshal(resource)
		if err != nil {
			return nil, err
		}
		txn, err := s.kv.Txn(ctx).
			If(clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(name)), "=", revision)).
			Then(clientv3.OpPut(s.fleetResourceKey(name), string(encoded))).
			Commit()
		if err != nil {
			return nil, err
		}
		if txn.Succeeded {
			return &resource, nil
		}
	}
}

// ListFleetObservations returns up to limit observations for name, newest
// sequence first.
func (s *V3OperationStore) ListFleetObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	if limit <= 0 || limit > controller.ObservationLimit {
		limit = controller.ObservationLimit
	}
	response, err := s.kv.Get(ctx, s.fleetObservationPrefix(name), clientv3.WithPrefix(),
		clientv3.WithSort(clientv3.SortByKey, clientv3.SortDescend), clientv3.WithLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	out := make([]controller.Observation, 0, len(response.Kvs))
	for _, kv := range response.Kvs {
		var obs controller.Observation
		if err := decodeV3Record(kv.Value, &obs); err != nil {
			return nil, fmt.Errorf("fleet observation is corrupt: %w", err)
		}
		out = append(out, obs)
	}
	return out, nil
}

// AppendFleetObservation ingests one observation for name, exactly as
// store.DB.AppendFleetObservation does: the server assigns Sequence and
// ReceivedAt, ObservedAt must fall in [ReceivedAt-24h, ReceivedAt+1m], and
// Applied = ObservedAt > the source's current watermark ObservedAt (M10).
func (s *V3OperationStore) AppendFleetObservation(ctx context.Context, name string, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	if !controller.ValidSource(source) || !controller.ValidateObservationStrings(evidenceRefs, reporter) {
		return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
	}
	if facts == nil {
		facts = controller.ObservationFacts{}
	}
	if evidenceRefs == nil {
		evidenceRefs = []string{}
	}
	observedAt = observedAt.UTC().Truncate(time.Microsecond)
	factsBytes, err := json.Marshal(facts)
	if err != nil {
		return nil, controller.Observation{}, err
	}
	if len(factsBytes) > controller.MaxObservationFactsBytes {
		return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
	}

	for {
		receivedAt := time.Now().UTC().Truncate(time.Microsecond)
		if !controller.ObservationWithinBounds(observedAt, receivedAt) {
			return nil, controller.Observation{}, ErrFleetObservationOutOfBounds
		}
		resource, revision, err := s.getFleetResourceWithRevision(ctx, name)
		if err != nil {
			return nil, controller.Observation{}, err
		}
		sequence := resource.ObservationSequence + 1
		wm, hasWatermark := resource.Watermarks[source]
		applied, nextWatermark := controller.ApplyObservation(wm, hasWatermark, observedAt, sequence)

		obs := controller.Observation{Sequence: sequence, Source: source, ObservedAt: observedAt, ReceivedAt: receivedAt, Applied: applied, Facts: facts, EvidenceRefs: evidenceRefs, Reporter: reporter}
		obsEncoded, err := json.Marshal(obs)
		if err != nil {
			return nil, controller.Observation{}, err
		}

		resource.ObservationSequence = sequence
		if applied {
			if resource.Watermarks == nil {
				resource.Watermarks = map[string]controller.Watermark{}
			}
			resource.Watermarks[source] = nextWatermark
		}
		resource.UpdatedAt = receivedAt
		resourceEncoded, err := json.Marshal(resource)
		if err != nil {
			return nil, controller.Observation{}, err
		}

		observationKey := s.fleetObservationKey(name, sequence)
		txn, err := s.kv.Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(name)), "=", revision),
			clientv3.Compare(clientv3.CreateRevision(observationKey), "=", 0),
		).Then(
			clientv3.OpPut(s.fleetResourceKey(name), string(resourceEncoded)),
			clientv3.OpPut(observationKey, string(obsEncoded)),
		).Commit()
		if err != nil {
			return nil, controller.Observation{}, err
		}
		if !txn.Succeeded {
			continue
		}
		if err := s.pruneFleetObservations(ctx, name, resource.Watermarks, txn.Header.Revision); err != nil {
			return nil, controller.Observation{}, err
		}
		return &resource, obs, nil
	}
}

// pruneFleetObservations deletes the oldest keys for name once its count
// exceeds controller.ObservationLimit, never one a current watermark names
// (M10). The delete Txn is conditional on the resource key still being the
// one this append wrote at resourceRevision, so watermarks is exactly the
// committed set; losing that race skips pruning (the later append prunes),
// which only widens the bound briefly and never deletes a watermark row.
func (s *V3OperationStore) pruneFleetObservations(ctx context.Context, name string, watermarks map[string]controller.Watermark, resourceRevision int64) error {
	response, err := s.kv.Get(ctx, s.fleetObservationPrefix(name), clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return err
	}
	excess := len(response.Kvs) - controller.ObservationLimit
	if excess <= 0 {
		return nil
	}
	keep := make(map[string]bool, len(watermarks))
	for _, wm := range watermarks {
		for _, sequence := range wm.Sequences() {
			keep[s.fleetObservationKey(name, sequence)] = true
		}
	}
	// Bounded well under etcd's default 128 ops per Txn.
	const maxDeletesPerTxn = 64
	var deletes []clientv3.Op
	for _, kv := range response.Kvs {
		if len(deletes) >= excess || len(deletes) >= maxDeletesPerTxn {
			break
		}
		if keep[string(kv.Key)] {
			continue
		}
		deletes = append(deletes, clientv3.OpDelete(string(kv.Key)))
	}
	if len(deletes) == 0 {
		return nil
	}
	_, err = s.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(name)), "=", resourceRevision)).
		Then(deletes...).
		Commit()
	return err
}

// fleetResourceHolderFacts re-derives HolderFacts for planID: its dispatch
// preparation/binding state and timestamps, its runner attempts and its
// abandonment record. It writes nothing (T5). It also returns a compare for
// every key it read, so the reconcile Txn fails if any of them changed:
// exact ModRevision (0 for an absent key, so a later create also fails it)
// for the single keys and every listed attempt (so a delete fails it), plus
// a prefix compare bounded by the attempt list's own read revision, so an
// attempt created after that read fails it too.
func (s *V3OperationStore) fleetResourceHolderFacts(ctx context.Context, planID string) (lifecycle.HolderFacts, int64, []clientv3.Cmp, error) {
	var holder lifecycle.HolderFacts
	var compares []clientv3.Cmp
	var runID int64
	exact := func(key string, response *clientv3.GetResponse) {
		var revision int64
		if len(response.Kvs) == 1 {
			revision = response.Kvs[0].ModRevision
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", revision))
	}

	preparationKey := s.fleetGitHubDispatchPreparationKey(planID)
	preparationResponse, err := s.kv.Get(ctx, preparationKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(preparationKey, preparationResponse)
	if len(preparationResponse.Kvs) == 1 {
		var preparation v3FleetGitHubDispatchPreparation
		if err := decodeV3Record(preparationResponse.Kvs[0].Value, &preparation); err != nil {
			return holder, 0, nil, fmt.Errorf("fleet GitHub dispatch preparation is corrupt: %w", err)
		}
		holder.DispatchState = "prepared"
		holder.DispatchCreatedAt = preparation.CreatedAt
	}
	bindingKey := s.fleetRunnerDispatchKey(planID)
	bindingResponse, err := s.kv.Get(ctx, bindingKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(bindingKey, bindingResponse)
	if len(bindingResponse.Kvs) == 1 {
		var binding v3FleetRunnerDispatch
		if err := decodeV3Record(bindingResponse.Kvs[0].Value, &binding); err != nil {
			return holder, 0, nil, fmt.Errorf("fleet runner dispatch binding is corrupt: %w", err)
		}
		holder.DispatchState = "bound"
		runID = binding.RunID
		holder.SubmissionStartedAt = binding.CreatedAt
	}

	attempts, attemptCompares, err := s.fleetResourceAttemptFacts(ctx, planID)
	if err != nil {
		return holder, 0, nil, err
	}
	holder.Attempts = attempts
	compares = append(compares, attemptCompares...)

	abandonedKey := s.fleetTargetAbandonedKey(planID)
	abandonedResponse, err := s.kv.Get(ctx, abandonedKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(abandonedKey, abandonedResponse)
	holder.Abandoned = len(abandonedResponse.Kvs) == 1
	return holder, runID, compares, nil
}

// fleetResourceAttemptFacts reads planID's runner attempts (oldest first)
// and returns the compares that fail the reconcile Txn if any of them
// changed, was deleted, or if one was created after this read.
func (s *V3OperationStore) fleetResourceAttemptFacts(ctx context.Context, planID string) ([]fleet.RunnerAttempt, []clientv3.Cmp, error) {
	var attempts []fleet.RunnerAttempt
	var compares []clientv3.Cmp
	attemptPrefix := s.fleetRunnerAttemptPrefix(planID)
	attemptResponse, err := s.kv.Get(ctx, attemptPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, nil, err
	}
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(attemptPrefix).WithPrefix(), "<", attemptResponse.Header.Revision+1))
	for _, kv := range attemptResponse.Kvs {
		var item fleet.RunnerAttempt
		if err := decodeV3Record(kv.Value, &item); err != nil {
			return nil, nil, err
		}
		if item.PlanID != planID || item.ID == "" {
			return nil, nil, fmt.Errorf("fleet runner attempt is corrupt")
		}
		item.SchemaVersion = fleet.RunnerAttemptSchemaVersion
		attempts = append(attempts, item)
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(string(kv.Key)), "=", kv.ModRevision))
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Attempt < attempts[j].Attempt })

	return attempts, compares, nil
}

// ReconcileFleetResource mirrors store.DB.ReconcileFleetResource: it gathers
// name's resource, the current authority epoch, the target fence (if any)
// and its holder's facts, and every watermark's own observation, calls
// derive, and commits a Txn comparing every key it read. A lost race
// re-reads everything fresh and retries (never blindly) up to
// fleetResourceReconcileRetries times, so etcd's version of M9 is "retries
// and recomputes from the newest inputs" where PG's is "serializes".
//
// afterRead, when non-nil, runs once -- only on the first attempt, after
// every read above but before derive is called -- so the shared conformance
// suite can inject a concurrent write at exactly that point
// (ReconcileWriteSeesLatestInputs). Production callers (WP12) pass nil.
func (s *V3OperationStore) ReconcileFleetResource(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	var lastErr error
	for attempt := 0; attempt < fleetResourceReconcileRetries; attempt++ {
		if attempt > 0 {
			// A small, attempt-scaled backoff before re-reading, so several
			// writers racing the same resource do not collide lockstep on
			// every retry (T4: still a fresh read and re-derive each time,
			// never a blind resend).
			time.Sleep(time.Duration(attempt) * 5 * time.Millisecond)
		}
		resource, resourceRevision, err := s.getFleetResourceWithRevision(ctx, name)
		if err != nil {
			return nil, err
		}
		epoch, epochRevision, err := s.fleetAuthorityEpochWithRevision(ctx)
		if err != nil {
			return nil, err
		}

		compares := []clientv3.Cmp{
			clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(name)), "=", resourceRevision),
			clientv3.Compare(clientv3.ModRevision(s.fleetAuthorityEpochKey()), "=", epochRevision),
		}

		var fence lifecycle.FenceFacts
		var holder lifecycle.HolderFacts
		var holderRunID int64
		var releasedAttempts []fleet.RunnerAttempt
		if resource.TargetID != "" {
			var fenceRevision int64
			fence, fenceRevision, err = s.GetFleetTargetFence(ctx, resource.TargetID)
			if err != nil {
				return nil, err
			}
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(resource.TargetID)), "=", fenceRevision))
			if fence.Held {
				var holderCompares []clientv3.Cmp
				holder, holderRunID, holderCompares, err = s.fleetResourceHolderFacts(ctx, fence.HolderPlanID)
				if err != nil {
					return nil, err
				}
				compares = append(compares, holderCompares...)
			}
			// The released plan's attempts feed derive's LastApplied (M11);
			// their keys are compared so a late attempt write retries us.
			if fence.LastRelease != nil && fence.LastRelease.Reason == "succeeded" {
				var releasedCompares []clientv3.Cmp
				releasedAttempts, releasedCompares, err = s.fleetResourceAttemptFacts(ctx, fence.LastRelease.PlanID)
				if err != nil {
					return nil, err
				}
				compares = append(compares, releasedCompares...)
			}
		}

		latest := map[string]controller.Observation{}
		tied := map[string][]controller.Observation{}
		sources := make([]string, 0, len(resource.Watermarks))
		for source := range resource.Watermarks {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		for _, source := range sources {
			for index, sequence := range resource.Watermarks[source].Sequences() {
				key := s.fleetObservationKey(name, sequence)
				response, err := s.kv.Get(ctx, key)
				if err != nil {
					return nil, err
				}
				if len(response.Kvs) == 0 {
					return nil, fmt.Errorf("fleet resource %q watermark for source %q has no observation key %d", name, source, sequence)
				}
				var obs controller.Observation
				if err := decodeV3Record(response.Kvs[0].Value, &obs); err != nil {
					return nil, fmt.Errorf("fleet observation is corrupt: %w", err)
				}
				if index == 0 {
					latest[source] = obs
				} else {
					tied[source] = append(tied[source], obs)
				}
				compares = append(compares, clientv3.Compare(clientv3.ModRevision(key), "=", response.Kvs[0].ModRevision))
			}
		}

		if attempt == 0 && afterRead != nil {
			afterRead()
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		status := derive(controller.Input{Resource: resource, Fence: fence, Holder: holder, HolderDispatchRunID: holderRunID, LastReleaseAttempts: releasedAttempts, AuthorityEpoch: epoch, LatestObservations: latest, TiedObservations: tied, Now: now})
		if resource.AuthorityEpoch == epoch && controller.StatusEquivalent(resource.Status, status) {
			// Unchanged (see controller.StatusEquivalent): skip the write so
			// the revision and etcd history do not grow, but still confirm
			// the inputs were one consistent snapshot with a write-free Txn
			// over the same compares; a torn read retries.
			txn, err := s.kv.Txn(ctx).If(compares...).Commit()
			if err != nil {
				return nil, err
			}
			if txn.Succeeded {
				return &resource, nil
			}
			lastErr = ErrFleetResourceReconcileConflict
			continue
		}

		resource.Status = status
		resource.Revision++
		resource.AuthorityEpoch = epoch
		resource.UpdatedAt = now
		encoded, err := json.Marshal(resource)
		if err != nil {
			return nil, err
		}

		txn, err := s.kv.Txn(ctx).If(compares...).Then(clientv3.OpPut(s.fleetResourceKey(name), string(encoded))).Commit()
		if err != nil {
			return nil, err
		}
		if txn.Succeeded {
			return &resource, nil
		}
		lastErr = ErrFleetResourceReconcileConflict
	}
	return nil, lastErr
}
