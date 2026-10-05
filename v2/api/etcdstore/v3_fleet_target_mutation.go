package etcdstore

// Signed Fleet target mutations on etcd (plan.md WP9a), the counterpart of
// store/fleet_target_mutation.go. The mutation (register, fence release,
// plan abandon) is decided from fresh reads by the same internal functions
// the unsigned test hooks call, and its compares and puts are committed in
// one Txn together with the signed acceptance, the operation record and the
// indexes: replayable and idempotent through the ordinary request identity,
// and attributed to that operations record (no second ledger). The
// server-gathered release evidence (acceptance.FleetTargetReleaseEvidence,
// never part of the client request or the fingerprint) is re-validated here
// with lifecycle.DecideRelease against freshly read holder facts, and the Txn
// pins every revision that decision read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/store"
)

func (s *V3OperationStore) acceptFleetTargetMutation(ctx context.Context, input store.OperationAcceptance) (store.AcceptedOperation, error) {
	if err := store.NormalizeFleetTargetMutationAcceptance(&input); err != nil {
		return store.AcceptedOperation{}, err
	}
	if s.policy.ReplayTTL > 0 {
		return store.AcceptedOperation{}, &store.AcceptanceValidationError{Reason: "etcd fleet target mutation replay expiry is not yet qualified"}
	}
	acceptance, err := s.normalize(input)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	key := s.acceptanceKey(acceptance.Identity)
	// A failed Txn committed nothing, so each round re-decides from fresh
	// reads (never retried blindly: T4); a Commit error is indeterminate.
	for range fleetTargetRegisterAttempts {
		if existing, err := s.loadAcceptance(ctx, key); err == nil {
			return s.replay(ctx, key, existing, acceptance.Identity, acceptance.Fingerprint)
		} else if !errors.Is(err, ErrNotFound) {
			return store.AcceptedOperation{}, err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		plan, err := s.planFleetTargetMutation(ctx, acceptance, now)
		if err != nil {
			return store.AcceptedOperation{}, err
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
		compares := append([]clientv3.Cmp{
			clientv3.Compare(clientv3.CreateRevision(key), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(s.opKey(acceptance.Operation.ID)), "=", 0),
		}, plan.compares...)
		puts := append([]clientv3.Op{
			clientv3.OpPut(key, string(acceptanceRecord)),
			clientv3.OpPut(s.opKey(acceptance.Operation.ID), string(operationRecord)),
			clientv3.OpPut(s.operationKindIndexKey(acceptance.Operation.Kind, now, acceptance.Operation.ID), acceptance.Operation.ID),
			clientv3.OpPut(s.operationAcceptanceIndexKey(acceptance.Operation.ID), key),
		}, plan.puts...)
		txn, err := s.kv.Txn(ctx).If(compares...).Then(puts...).Commit()
		if err != nil {
			return store.AcceptedOperation{}, &store.AcceptanceIndeterminateError{Err: err}
		}
		if txn.Succeeded {
			return accepted, nil
		}
	}
	return store.AcceptedOperation{}, fmt.Errorf("fleet target mutation changed concurrently")
}

func (s *V3OperationStore) planFleetTargetMutation(ctx context.Context, acceptance store.OperationAcceptance, now time.Time) (fleetTargetMutationPlan, error) {
	admission := acceptance.FleetTargetMutation
	operationID := acceptance.Operation.ID
	switch admission.Kind {
	case store.FleetTargetMutationRegister:
		identity := lifecycle.TargetIdentity{Provider: admission.Provider, ProviderAccount: admission.ProviderAccount, StateBackend: admission.StateBackend}
		clusterNames, environments, unique, err := classifyFleetTargetAliases(admission.Aliases)
		if err != nil {
			return fleetTargetMutationPlan{}, err
		}
		_, plan, err := s.prepareFleetTargetRegistration(ctx, identity, admission.TargetID, operationID, clusterNames, environments, unique)
		return plan, err
	case store.FleetTargetMutationRelease:
		release := admission.Release
		return s.planFleetTargetRelease(ctx, admission.TargetID, release.ExpectedGeneration, release.ReleaseMode(), acceptance.FleetTargetReleaseEvidence.TerminalProof(), operationID, now)
	case store.FleetTargetMutationAbandonPlan:
		// The server derives cluster, environment and nonce from the plan's
		// own records; the caller claims none of them.
		cluster, err := s.fleetCapacityPlanCluster(ctx, admission.PlanID)
		if err != nil {
			return fleetTargetMutationPlan{}, err
		}
		var environment, nonceSHA256 string
		response, err := s.kv.Get(ctx, s.fleetGitHubDispatchPreparationKey(admission.PlanID))
		if err != nil {
			return fleetTargetMutationPlan{}, err
		}
		if len(response.Kvs) == 1 {
			var preparation v3FleetGitHubDispatchPreparation
			if err := decodeV3Record(response.Kvs[0].Value, &preparation); err != nil {
				return fleetTargetMutationPlan{}, err
			}
			environment, nonceSHA256 = preparation.FleetEnvironment, preparation.DispatchNonceSHA256
		}
		if nonceSHA256 == "" {
			return fleetTargetMutationPlan{}, lifecycle.ErrFleetTargetReleaseEvidenceMismatch
		}
		return s.planFleetTargetAbandon(ctx, admission.PlanID, nonceSHA256, cluster, environment, operationID, acceptance.FleetTargetReleaseEvidence.TerminalProof().ListingSnapshotSHA256, now)
	}
	return fleetTargetMutationPlan{}, &store.AcceptanceValidationError{Reason: "fleet target mutation kind is invalid"}
}
