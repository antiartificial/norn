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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// ErrFleetResourceNotFound mirrors store.ErrFleetResourceNotFound.
var ErrFleetResourceNotFound = fmt.Errorf("fleet resource not found")

// ErrFleetResourceTargetMismatch mirrors store.ErrFleetResourceTargetMismatch.
var ErrFleetResourceTargetMismatch = fmt.Errorf("fleet resource is already bound to a different target")

// ErrFleetObservationOutOfBounds mirrors store.ErrFleetObservationOutOfBounds.
var ErrFleetObservationOutOfBounds = fmt.Errorf("fleet observation is out of bounds")

// ErrFleetResourceDesiredInvalid mirrors store.ErrFleetResourceDesiredInvalid.
var ErrFleetResourceDesiredInvalid = fmt.Errorf("fleet resource desired revision is invalid")

// ErrFleetResourceRevisionConflict mirrors store.ErrFleetResourceRevisionConflict.
var ErrFleetResourceRevisionConflict = fmt.Errorf("fleet resource revision does not match expectedRevision")

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

// SetDesiredFleetResourceIf accepts a new desired revision (Q8) with the operator's
// expectedRevision CAS (WP13), mirroring store.DB.SetDesiredFleetResourceIf:
// ErrFleetResourceRevisionConflict unless Revision is exactly
// expectedRevision, and a successful write bumps Revision by one.
func (s *V3OperationStore) SetDesiredFleetResourceIf(ctx context.Context, name string, expectedRevision int64, next controller.DesiredRevision) (*controller.Resource, error) {
	return s.setDesiredFleetResource(ctx, name, &expectedRevision, next)
}

func (s *V3OperationStore) setDesiredFleetResource(ctx context.Context, name string, expectedRevision *int64, next controller.DesiredRevision) (*controller.Resource, error) {
	if err := controller.ValidateDesired(next); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrFleetResourceDesiredInvalid, err)
	}
	for {
		resource, revision, err := s.getFleetResourceWithRevision(ctx, name)
		if err != nil {
			return nil, err
		}
		if expectedRevision != nil && resource.Revision != *expectedRevision {
			return nil, ErrFleetResourceRevisionConflict
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
		if expectedRevision != nil {
			resource.Revision++
		}

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

	offlineKey := s.offlineDispatchKey(planID)
	offlineResponse, err := s.kv.Get(ctx, offlineKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(offlineKey, offlineResponse)
	var offlineBinding offline.Binding
	validOfflineBinding := false
	if len(offlineResponse.Kvs) == 1 {
		holder.OfflineReservation = true
		binding, decodeErr := offline.DecodeBinding(offlineResponse.Kvs[0].Value)
		if decodeErr != nil || binding.PlanID != planID || holder.DispatchState != "" || len(attempts) != 0 {
			holder.OfflineEvidenceIncomplete = true
		} else {
			offlineBinding, validOfflineBinding = binding, true
			holder.OfflineTargetID = binding.TargetID
			holder.OfflineNonceSHA256 = binding.DispatchNonceSHA256
			holder.OfflineFenceGeneration = binding.FenceGeneration
			holder.SubmissionStartedAt = binding.CreatedAt
			ownerKey := s.offlineOwnerConsumptionKey(binding.OwnerAuthorizationID)
			ownerResponse, err := s.kv.Get(ctx, ownerKey)
			if err != nil {
				return holder, 0, nil, err
			}
			exact(ownerKey, ownerResponse)
			holder.OfflineEvidenceIncomplete = len(ownerResponse.Kvs) != 1 ||
				string(ownerResponse.Kvs[0].Value) != binding.PlanID+":"+binding.OwnerEnvelopeSHA256
		}
	}
	offlineAttemptKey := s.offlineAttemptKey(planID)
	offlineAttemptResponse, err := s.kv.Get(ctx, offlineAttemptKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(offlineAttemptKey, offlineAttemptResponse)
	if len(offlineAttemptResponse.Kvs) == 1 {
		if !validOfflineBinding {
			holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
		}
		attempt, decodeErr := decodeOfflineAttempt(offlineAttemptResponse.Kvs[0].Value, planID)
		var digest [32]byte
		if validOfflineBinding {
			digest = sha256.Sum256(offlineResponse.Kvs[0].Value)
		}
		if decodeErr != nil || !validOfflineBinding || attempt.BindingSHA256 != hex.EncodeToString(digest[:]) ||
			attempt.TargetID != offlineBinding.TargetID || attempt.FenceGeneration != offlineBinding.FenceGeneration ||
			attempt.PrincipalSubject != offlineBinding.RunnerPrincipalSubject {
			holder.OfflineEvidenceIncomplete = true
		} else {
			holder.OfflineQueuedAttempt = true
		}
	}
	checkpointKey := s.offlineCheckpointKey(planID)
	checkpointHead, err := s.kv.Get(ctx, checkpointKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(checkpointKey, checkpointHead)
	eventKey := s.offlineCheckpointEventKey(planID, 1)
	checkpointEvent, err := s.kv.Get(ctx, eventKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(eventKey, checkpointEvent)
	var headRaw, eventRaw []byte
	if len(checkpointHead.Kvs) == 1 {
		headRaw = checkpointHead.Kvs[0].Value
	}
	if len(checkpointEvent.Kvs) == 1 {
		eventRaw = checkpointEvent.Kvs[0].Value
	}
	checkpoint, checkpointErr := decodeOfflineCheckpointPair(headRaw, eventRaw, planID)
	if errors.Is(checkpointErr, ErrOfflineAttemptCorrupt) {
		holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
	} else if checkpointErr != nil && !errors.Is(checkpointErr, ErrNotFound) {
		return holder, 0, nil, checkpointErr
	} else if checkpointErr == nil {
		if len(offlineAttemptResponse.Kvs) != 1 || !validOfflineBinding ||
			checkpoint.FenceGeneration != offlineBinding.FenceGeneration {
			holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
		} else if attempt, err := decodeOfflineAttempt(offlineAttemptResponse.Kvs[0].Value, planID); err != nil || checkpoint.AttemptID != attempt.ID ||
			(checkpoint.Prechange != nil && checkpoint.Prechange.ProviderTopologyVerified &&
				(offlineBinding.SchemaVersion != offline.BindingSchemaV2 || checkpoint.Prechange.BindingSHA256 != attempt.BindingSHA256)) {
			holder.OfflineEvidenceIncomplete = true
		}
	}
	grantKey := s.offlineEffectGrantKey(planID)
	grantHead, err := s.kv.Get(ctx, grantKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(grantKey, grantHead)
	issuedKey := s.offlineEffectGrantEventKey(planID, 1)
	grantIssued, err := s.kv.Get(ctx, issuedKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(issuedKey, grantIssued)
	consumedKey := s.offlineEffectGrantEventKey(planID, 2)
	grantConsumed, err := s.kv.Get(ctx, consumedKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(consumedKey, grantConsumed)
	var grantHeadRaw, grantIssuedRaw, grantConsumedRaw []byte
	if len(grantHead.Kvs) == 1 {
		grantHeadRaw = grantHead.Kvs[0].Value
	}
	if len(grantIssued.Kvs) == 1 {
		grantIssuedRaw = grantIssued.Kvs[0].Value
	}
	if len(grantConsumed.Kvs) == 1 {
		grantConsumedRaw = grantConsumed.Kvs[0].Value
	}
	grant, grantErr := decodeOfflineEffectGrantPair(grantHeadRaw, grantIssuedRaw, grantConsumedRaw, planID)
	if errors.Is(grantErr, ErrOfflineAttemptCorrupt) {
		holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
	} else if grantErr != nil && !errors.Is(grantErr, ErrNotFound) {
		return holder, 0, nil, grantErr
	} else if grantErr == nil {
		if !validOfflineBinding || len(offlineAttemptResponse.Kvs) != 1 || checkpointErr != nil ||
			grant.TargetID != offlineBinding.TargetID || grant.FenceGeneration != offlineBinding.FenceGeneration ||
			grant.PrechangeSHA256 != checkpoint.EvidenceSHA256 || grant.Backend != offlineBinding.Backend ||
			grant.SourceCommit != offlineBinding.SourceCommit || grant.SavedPlanSHA256 != offlineBinding.SavedPlanSHA256 ||
			grant.EffectSHA256 != offlineBinding.EffectSHA256 || grant.ProviderAccountUUID != offlineBinding.ProviderAccountUUID ||
			grant.NodeName != offlineBinding.NodeName || grant.DropletID != offlineBinding.DropletID {
			holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
		} else if attempt, decodeErr := decodeOfflineAttempt(offlineAttemptResponse.Kvs[0].Value, planID); decodeErr != nil ||
			grant.AttemptID != attempt.ID || grant.BindingSHA256 != attempt.BindingSHA256 {
			holder.OfflineEvidenceIncomplete = true
		}
	}

	abandonedKey := s.fleetTargetAbandonedKey(planID)
	abandonedResponse, err := s.kv.Get(ctx, abandonedKey)
	if err != nil {
		return holder, 0, nil, err
	}
	exact(abandonedKey, abandonedResponse)
	holder.Abandoned = len(abandonedResponse.Kvs) == 1
	if holder.DispatchState == "" && !holder.OfflineReservation {
		// A converge plan on the executor lane has no dispatch keys and is
		// bound once its first attempt exists (plan-5-7 section 2.6).
		converge, planRevision, err := s.fleetConvergePlanOf(ctx, planID)
		if err != nil {
			return holder, 0, nil, err
		}
		compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", planRevision))
		if converge != nil {
			holder.DispatchState = fleetExecutorHolderDispatchState
		}
		if holder.DispatchState == "" {
			plan, revision, err := s.load(ctx, planID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return holder, 0, nil, err
			}
			if err == nil {
				compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.opKey(planID)), "=", revision))
				var typed fleet.CapacityPlan
				if validOfflineContractionPlan(plan.Operation, planID, &typed) {
					holder.OfflineReservation, holder.OfflineEvidenceIncomplete = true, true
				}
			}
		}
	}
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
		var releasedConverge *executor.ConvergePlan
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
				var planRevision int64
				releasedConverge, planRevision, err = s.fleetConvergePlanOf(ctx, fence.LastRelease.PlanID)
				if err != nil {
					return nil, err
				}
				compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.opKey(fence.LastRelease.PlanID)), "=", planRevision))
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
		status = fleetLastAppliedFromConvergePlan(status, fence, releasedConverge)
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

// fleetLastAppliedFromConvergePlan is store.lastAppliedFromConvergePlan: when
// the fence's succeeded release was a converge plan, the last-applied commit
// and generation are the converge plan's own (commitSha, generation), not a
// lookup of the attempt's commit in the desired history. Any other plan, and
// any release that is not "succeeded", leaves status untouched.
func fleetLastAppliedFromConvergePlan(status controller.Status, fence lifecycle.FenceFacts, converge *executor.ConvergePlan) controller.Status {
	if converge == nil || fence.LastRelease == nil || fence.LastRelease.Reason != "succeeded" ||
		status.LastApplied == nil || status.LastApplied.PlanID != fence.LastRelease.PlanID {
		return status
	}
	applied := *status.LastApplied
	applied.Generation, applied.CommitSHA, applied.Reason = converge.Generation, converge.CommitSHA, ""
	status.LastApplied = &applied
	status.LastAppliedGeneration = converge.Generation
	return status
}

// ListWatchedResources returns only the resources whose stored executionMode
// is controller, for the converge watcher (controller.WatchedResourceSource).
// Cluster is the resource target's cluster alias; LastApplied is
// status.lastApplied.commitSha, or "" when it is absent or carries a reason.
// A controller resource whose target or cluster alias is missing is skipped.
// Mode is enforced here, at plan recording, never in an execution Txn (T7).
func (s *V3OperationStore) ListWatchedResources(ctx context.Context) ([]controller.WatchedResource, error) {
	prefix := s.fleetResourceKey("")
	response, err := s.kv.Get(ctx, prefix, clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	var out []controller.WatchedResource
	for _, kv := range response.Kvs {
		var resource controller.Resource
		if err := decodeV3Record(kv.Value, &resource); err != nil {
			return nil, fmt.Errorf("fleet resource is corrupt: %w", err)
		}
		if resource.ExecutionMode != executor.ModeController || resource.TargetID == "" {
			continue
		}
		target, err := s.GetFleetTarget(ctx, resource.TargetID)
		if err != nil {
			return nil, err
		}
		cluster := ""
		if target != nil {
			for _, alias := range target.Aliases {
				if strings.HasPrefix(alias, "cluster:") {
					cluster = strings.TrimPrefix(alias, "cluster:")
					break
				}
			}
		}
		if cluster == "" {
			continue
		}
		watched := controller.WatchedResource{Name: resource.Name, Cluster: cluster, Repository: resource.Desired.Repository, DesiredGeneration: resource.Desired.Generation}
		if applied := resource.Status.LastApplied; applied != nil && applied.Reason == "" {
			watched.LastApplied = applied.CommitSHA
		}
		out = append(out, watched)
	}
	return out, nil
}

// SetFleetResourceExecutionMode applies and records a
// fleet.resource.execution-mode operation in ONE Txn (plan-5-7, "Hand-offs
// from the E4b review"). Request identity first: a replay with the same body
// returns the stored operation and applies nothing; the same key with another
// body is a store.AcceptanceConflictError (idempotency_key_reused). Otherwise
// the resource, its target fence and the capacity-plan index are read, and the
// Txn compares each of them: a held fence (either lane) refuses with
// executor.CodeFleetExecutionModeFenceHeld, an unknown resource is
// ErrFleetResourceNotFound, a stale expectedRevision is
// ErrFleetResourceRevisionConflict. The mode is written into the resource
// record and the revision bumped only when the effective mode changes. A
// switch to github abandons (executor.HolderNonceSHA256) every converge plan
// of the resource that has no attempt, in the same Txn; start already refuses
// abandoned plans and never reads the mode (T7).
//
// The previousMode in the operation metadata is part of the signed request, so
// a replay rebuilds the request with the stored operation's previousMode
// before comparing fingerprints.
func (s *V3OperationStore) SetFleetResourceExecutionMode(ctx context.Context, identity store.OperationRequestIdentity, audit store.AcceptanceAuditContext, request executor.ExecutionModeRequest) (store.AcceptedOperation, error) {
	if err := request.Validate(); err != nil {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: err.Error()}
	}
	name := strings.TrimSpace(identity.Resource)
	if identity.Kind != executor.KindResourceExecutionMode || !controller.ValidResourceName(name) {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "fleet resource execution-mode identity is invalid"}
	}
	if s.policy.ReplayTTL > 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd fleet execution-mode replay expiry is not yet qualified"}
	}
	build := func(previousMode string, now time.Time) (store.OperationAcceptance, error) {
		finished := now
		op := model.Operation{
			ID: uuid.NewString(), Kind: executor.KindResourceExecutionMode, Ref: name, Status: model.OperationSucceeded,
			Source: "fleet-execution-mode", Risk: "changes which lane may apply converge plans for the resource",
			Message:   "fleet resource execution mode " + request.Mode,
			Payload:   map[string]interface{}{"resource": name, "mode": request.Mode, "expectedRevision": request.ExpectedRevision},
			Metadata:  map[string]interface{}{"resource": name, "mode": request.Mode, "previousMode": previousMode},
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished, MaxAttempts: 1,
		}
		acceptance := store.OperationAcceptance{
			Identity: identity, Operation: op, Audit: audit,
			Semantics: map[string]interface{}{"action": executor.KindResourceExecutionMode, "resource": name, "mode": request.Mode, "expectedRevision": request.ExpectedRevision},
		}
		acceptance.Identity.Resource = name
		var err error
		if acceptance.Fingerprint, err = store.CanonicalOperationRequestFingerprint(acceptance); err != nil {
			return store.OperationAcceptance{}, err
		}
		return s.normalize(acceptance)
	}

	for round := 0; round < fleetExecutorStartRounds; round++ {
		now := time.Now().UTC().Truncate(time.Microsecond)
		probe, err := build("", now)
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		key := s.acceptanceKey(probe.Identity)
		if existing, err := s.loadAcceptance(ctx, key); err == nil {
			previous, _ := existing.record.Accepted.Operation.Metadata["previousMode"].(string)
			stored, err := build(previous, now)
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			return s.replay(ctx, key, existing, stored.Identity, stored.Fingerprint)
		} else if !errors.Is(err, ErrNotFound) {
			return store.AcceptedOperation{}, err
		}

		resource, resourceRevision, err := s.getFleetResourceWithRevision(ctx, name)
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		compares := []clientv3.Cmp{clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(name)), "=", resourceRevision)}
		if resource.TargetID != "" {
			fence, fenceRevision, err := s.GetFleetTargetFence(ctx, resource.TargetID)
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			if fence.Held {
				return store.AcceptedOperation{}, &executor.Error{Code: executor.CodeFleetExecutionModeFenceHeld, Reason: "the resource's target fence is held; wait for it to be free"}
			}
			compares = append(compares, clientv3.Compare(clientv3.ModRevision(s.fleetTargetFenceKey(resource.TargetID)), "=", fenceRevision))
		}
		if request.ExpectedRevision != resource.Revision {
			return store.AcceptedOperation{}, ErrFleetResourceRevisionConflict
		}
		previousMode := controller.EffectiveExecutionMode(resource.ExecutionMode)
		acceptance, err := build(previousMode, now)
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		var puts []clientv3.Op
		if previousMode != request.Mode {
			resource.ExecutionMode = request.Mode
			resource.Revision++
			resource.UpdatedAt = now
			encoded, err := json.Marshal(resource)
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			puts = append(puts, clientv3.OpPut(s.fleetResourceKey(name), string(encoded)))
		}
		if request.Mode == executor.ModeGitHub {
			abandonCompares, abandonPuts, err := s.planAbandonUnstartedConvergePlans(ctx, name, resource.TargetID, acceptance.Operation.ID, now)
			if err != nil {
				return store.AcceptedOperation{}, err
			}
			compares, puts = append(compares, abandonCompares...), append(puts, abandonPuts...)
		}

		identityID, intentID := uuid.NewString(), uuid.NewString()
		intent, err := store.SealOperationAcceptance(ctx, s.signer, acceptance, identityID, intentID, now)
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		accepted := store.AcceptedOperation{Operation: acceptance.Operation, RequestIdentityID: identityID, AcceptanceIntentID: intentID, Intent: intent}
		acceptanceRecord, err := json.Marshal(v3Acceptance{Identity: acceptance.Identity, Accepted: accepted})
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		operationRecord, err := json.Marshal(v3Record{Operation: acceptance.Operation})
		if err != nil {
			return store.AcceptedOperation{}, err
		}
		compares = append(compares,
			clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0))
		puts = append(puts,
			clientv3.OpPut(key, string(acceptanceRecord)),
			clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)),
			clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, now, acceptance.Operation.ID), acceptance.Operation.ID),
			clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), key))
		txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
		if err != nil {
			return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
		}
		if txn.Succeeded {
			return accepted, nil
		}
		// Lost the Txn: nothing committed; the next round re-decides from
		// fresh reads (a concurrent replay of this key is found first).
	}
	return store.AcceptedOperation{}, fmt.Errorf("fleet resource execution mode changed concurrently")
}

// planAbandonUnstartedConvergePlans returns the compares and puts that abandon
// every converge plan of resource that has no runner attempt, using
// executor.HolderNonceSHA256 (fleetTargetBindInputs has no nonce for them).
// The capacity-plan index is compared so a plan recorded after the scan fails
// the Txn, each plan's attempts prefix so a start after the scan does too, and
// each abandoned key must still be absent.
func (s *V3OperationStore) planAbandonUnstartedConvergePlans(ctx context.Context, resource, targetID, operationID string, now time.Time) ([]clientv3.Cmp, []clientv3.Op, error) {
	prefix := s.operationKindIndexPrefix("fleet.capacity-plan")
	end := clientv3.GetPrefixRangeEnd(prefix)
	start := prefix
	var firstRevision int64
	var compares []clientv3.Cmp
	var puts []clientv3.Op
	for {
		response, err := s.kv.Get(ctx, start, clientv3.WithRange(end), clientv3.WithLimit(100), clientv3.WithSort(clientv3.SortByKey, clientv3.SortAscend))
		if err != nil {
			return nil, nil, err
		}
		if firstRevision == 0 {
			firstRevision = response.Header.Revision
		}
		for _, kv := range response.Kvs {
			record, _, err := s.load(ctx, string(kv.Value))
			if err != nil {
				return nil, nil, fmt.Errorf("load indexed operation %q: %w", string(kv.Value), err)
			}
			op := record.Operation
			if op.Ref != resource || op.Status != model.OperationSucceeded {
				continue
			}
			converge, _, _ := parseFleetConvergePlanPayload(op.Payload)
			if converge == nil || converge.Resource != resource {
				continue
			}
			if abandoned, err := s.checkFleetTargetPlanAbandoned(ctx, op.ID); err != nil {
				return nil, nil, err
			} else if abandoned {
				continue
			}
			attempts, err := s.kv.Get(ctx, s.fleetRunnerAttemptPrefix(op.ID), clientv3.WithPrefix(), clientv3.WithCountOnly())
			if err != nil {
				return nil, nil, err
			}
			if attempts.Count != 0 {
				continue
			}
			record2, err := json.Marshal(v3FleetTargetAbandoned{PlanID: op.ID, NonceSHA256: executor.HolderNonceSHA256(op.ID), TargetID: targetID, OperationID: operationID, AbandonedAt: now})
			if err != nil {
				return nil, nil, err
			}
			compares = append(compares, s.abandonedCompare(op.ID),
				clientv3.Compare(clientv3.CreateRevision(s.fleetRunnerAttemptPrefix(op.ID)).WithPrefix(), "=", 0))
			puts = append(puts, clientv3.OpPut(s.fleetTargetAbandonedKey(op.ID), string(record2)))
		}
		if !response.More || len(response.Kvs) == 0 {
			break
		}
		start = string(response.Kvs[len(response.Kvs)-1].Key) + "\x00"
	}
	compares = append(compares, clientv3.Compare(clientv3.ModRevision(prefix).WithPrefix(), "<", firstRevision+1))
	return compares, puts, nil
}

// ErrFleetResourceModeChanged means the resource key changed between the mode
// read and the commit of AcceptFleetConvergePlan; the mode may still match, so
// the caller retries (AcceptFleetConvergePlan does, a bounded number of times).
var ErrFleetResourceModeChanged = errors.New("fleet resource changed while its execution mode was checked")

// fleetResourceModeCompare reads the resource named by a.Operation.Ref and
// returns the compare that pins its ModRevision, or controller.ErrNotWatched
// when its effective execution mode is not a.RequireFleetResourceMode (a
// missing resource included).
func (s *V3OperationStore) fleetResourceModeCompare(ctx context.Context, a store.OperationAcceptance) (clientv3.Cmp, error) {
	resource, revision, err := s.getFleetResourceWithRevision(ctx, a.Operation.Ref)
	if errors.Is(err, ErrFleetResourceNotFound) {
		return clientv3.Cmp{}, controller.ErrNotWatched
	}
	if err != nil {
		return clientv3.Cmp{}, err
	}
	if controller.EffectiveExecutionMode(resource.ExecutionMode) != a.RequireFleetResourceMode {
		return clientv3.Cmp{}, controller.ErrNotWatched
	}
	return clientv3.Compare(clientv3.ModRevision(s.fleetResourceKey(a.Operation.Ref)), "=", revision), nil
}

// AcceptFleetConvergePlan is the etcd converge-plan recorder's commit (E6
// calls it from its controller.ConvergePlanRecorder). It accepts a
// fleet.capacity-plan operation only while the resource named by a.Operation.Ref
// is in controller mode, in the same Txn: the resource key's ModRevision is
// compared, so a switch to github that lands after the watcher's read either
// fails this Txn (the next round sees github and refuses with
// controller.ErrNotWatched) or commits after it, and then abandons the plan
// (SetFleetResourceExecutionMode compares the capacity-plan index). A replay
// of an accepted key is answered before the mode is read. The resource key
// also changes on every reconcile write, so a lost compare is retried.
func (s *V3OperationStore) AcceptFleetConvergePlan(ctx context.Context, a store.OperationAcceptance) (store.AcceptedOperation, error) {
	if a.Operation.Kind != "fleet.capacity-plan" || a.Identity.Kind != "fleet.capacity-plan" {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "converge plan recording requires a fleet.capacity-plan operation"}
	}
	a.RequireFleetResourceMode = executor.ModeController
	for round := 0; ; round++ {
		accepted, err := s.Accept(ctx, a)
		if !errors.Is(err, ErrFleetResourceModeChanged) || round+1 >= fleetExecutorStartRounds {
			return accepted, err
		}
	}
}
