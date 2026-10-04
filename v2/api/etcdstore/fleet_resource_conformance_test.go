package etcdstore

// Package etcdstore (not etcdstore_test), mirroring
// etcdstore/fleet_target_conformance_test.go, so the harness below can
// reach V3OperationStore's private key builders directly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet"
	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// fleetResourceTestHarness implements fleettest.ResourceHarness against a
// real etcd-backed V3OperationStore.
type fleetResourceTestHarness struct {
	store *V3OperationStore
	// holderTargetID/holderPlanID remember SeedHolderBinding's keys so
	// SnapshotHolderBinding and AddHolderAttempt can address them later.
	holderTargetID, holderPlanID string
}

func (h *fleetResourceTestHarness) NewResource(ctx context.Context) (string, error) {
	id := uuid.NewString()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "resource-" + id, StateBackend: "s3://resource-conformance-" + id + "/state"}
	target, err := h.store.RegisterFleetTarget(ctx, identity, []string{"cluster:resource-" + id}, "op-resource-"+id)
	if err != nil {
		return "", err
	}
	name := "resource-" + id
	if _, err := h.store.EnsureFleetResource(ctx, name, target.TargetID); err != nil {
		return "", err
	}
	return name, nil
}

func (h *fleetResourceTestHarness) SetDesired(ctx context.Context, name string, next controller.DesiredRevision) (*controller.Resource, error) {
	return h.store.setDesiredFleetResource(ctx, name, nil, next)
}

func (h *fleetResourceTestHarness) IsDesiredInvalid(err error) bool {
	return errors.Is(err, ErrFleetResourceDesiredInvalid)
}

func (h *fleetResourceTestHarness) GetResource(ctx context.Context, name string) (*controller.Resource, error) {
	return h.store.GetFleetResource(ctx, name)
}

func (h *fleetResourceTestHarness) AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	return h.store.AppendFleetObservation(ctx, name, source, observedAt, facts, evidenceRefs, reporter)
}

func (h *fleetResourceTestHarness) IsObservationOutOfBounds(err error) bool {
	return errors.Is(err, ErrFleetObservationOutOfBounds)
}

func (h *fleetResourceTestHarness) ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	return h.store.ListFleetObservations(ctx, name, limit)
}

func (h *fleetResourceTestHarness) Reconcile(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	return h.store.ReconcileFleetResource(ctx, name, derive, afterRead)
}

func (h *fleetResourceTestHarness) Serializes() bool { return false }

func (h *fleetResourceTestHarness) Epoch(ctx context.Context) (int64, error) {
	return h.store.FleetAuthorityEpoch(ctx)
}

func (h *fleetResourceTestHarness) AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	return h.store.AdvanceFleetAuthorityEpoch(ctx, expected, reason)
}

func (h *fleetResourceTestHarness) SeedHolderBinding(ctx context.Context, name string) (string, error) {
	resource, err := h.store.GetFleetResource(ctx, name)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	planID := "plan-" + id
	epoch, err := h.store.FleetAuthorityEpoch(ctx)
	if err != nil {
		return "", err
	}
	fence := v3FleetTargetFence{Generation: 1, Held: true, HolderPlanID: planID, HolderNonceSHA256: "nonce-" + id, AuthorityEpoch: epoch, Revision: 1}
	encodedFence, err := json.Marshal(fence)
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.fleetTargetFenceKey(resource.TargetID), string(encodedFence)); err != nil {
		return "", err
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	plan := model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		StartedAt: now, FinishedAt: &now, MaxAttempts: 1,
		Payload: map[string]interface{}{"cluster": "holder-" + id}, Metadata: map[string]interface{}{},
	}
	planRecord, err := json.Marshal(v3Record{Operation: plan})
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.opKey(planID), string(planRecord)); err != nil {
		return "", err
	}

	binding := v3FleetRunnerDispatch{
		FleetRunnerDispatchBinding: FleetRunnerDispatchBinding{PlanID: planID, PlanSHA256: "sha-" + id, ApprovedHeadSHA: "head-" + id, DispatchNonceSHA256: "nonce-" + id, RunID: fleettest.SeedDispatchRunID},
		CreatedAt:                  now,
	}
	encodedBinding, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.fleetRunnerDispatchKey(planID), string(encodedBinding)); err != nil {
		return "", err
	}

	attempt := fleet.RunnerAttempt{ID: "attempt-" + id, PlanID: planID, Attempt: 1, RunnerAttemptID: "runner-" + id, Status: "running", CurrentPhase: "prechange_verified", StartedAt: now, HeartbeatAt: now, HeartbeatTimeoutSeconds: 600, HeartbeatExpiresAt: now.Add(10 * time.Minute), UpdatedAt: now}
	encodedAttempt, err := json.Marshal(attempt)
	if err != nil {
		return "", err
	}
	if _, err := h.store.kv.Put(ctx, h.store.fleetRunnerAttemptKey(planID, attempt.ID), string(encodedAttempt)); err != nil {
		return "", err
	}

	h.holderTargetID, h.holderPlanID = resource.TargetID, planID
	return h.SnapshotHolderBinding(ctx)
}

func (h *fleetResourceTestHarness) ReconcilerStore() controller.ReconcilerStore { return h.store }

func (h *fleetResourceTestHarness) IsNotFound(err error) bool {
	return errors.Is(err, ErrFleetResourceNotFound)
}

// SucceedHolder completes the seeded holder's attempt at commitSHA and frees
// the fence with a succeeded release.
func (h *fleetResourceTestHarness) SucceedHolder(ctx context.Context, commitSHA string) error {
	attempts, _, err := h.store.listFleetRunnerAttempts(ctx, h.holderPlanID)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, attempt := range attempts {
		attempt.Status, attempt.CommitSHA, attempt.FinishedAt, attempt.UpdatedAt = "succeeded", commitSHA, &now, now
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return err
		}
		if _, err := h.store.kv.Put(ctx, h.store.fleetRunnerAttemptKey(h.holderPlanID, attempt.ID), string(encoded)); err != nil {
			return err
		}
	}
	response, err := h.store.kv.Get(ctx, h.store.fleetTargetFenceKey(h.holderTargetID))
	if err != nil || len(response.Kvs) != 1 {
		return fmt.Errorf("fence read: %v", err)
	}
	var fence v3FleetTargetFence
	if err := json.Unmarshal(response.Kvs[0].Value, &fence); err != nil {
		return err
	}
	fence.Held, fence.HolderPlanID, fence.HolderNonceSHA256 = false, "", ""
	fence.LastReleasePlanID, fence.LastReleaseReason, fence.LastReleaseAt = h.holderPlanID, "succeeded", &now
	fence.Revision++
	encoded, err := json.Marshal(fence)
	if err != nil {
		return err
	}
	_, err = h.store.kv.Put(ctx, h.store.fleetTargetFenceKey(h.holderTargetID), string(encoded))
	return err
}

func (h *fleetResourceTestHarness) AddHolderAttempt(ctx context.Context) error {
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := uuid.NewString()
	attempt := fleet.RunnerAttempt{ID: "attempt-" + id, PlanID: h.holderPlanID, Attempt: 2, RunnerAttemptID: "runner-" + id, Status: "running", CurrentPhase: "prechange_verified", StartedAt: now, HeartbeatAt: now, UpdatedAt: now}
	encoded, err := json.Marshal(attempt)
	if err != nil {
		return err
	}
	_, err = h.store.kv.Put(ctx, h.store.fleetRunnerAttemptKey(h.holderPlanID, attempt.ID), string(encoded))
	return err
}

func (h *fleetResourceTestHarness) SnapshotHolderBinding(ctx context.Context) (string, error) {
	fence, _, err := h.store.GetFleetTargetFence(ctx, h.holderTargetID)
	if err != nil {
		return "", err
	}
	bindingResponse, err := h.store.kv.Get(ctx, h.store.fleetRunnerDispatchKey(h.holderPlanID))
	if err != nil {
		return "", err
	}
	attempts, _, err := h.store.listFleetRunnerAttempts(ctx, h.holderPlanID)
	if err != nil {
		return "", err
	}
	var bindingValue string
	if len(bindingResponse.Kvs) == 1 {
		bindingValue = string(bindingResponse.Kvs[0].Value)
	}
	encoded, err := json.Marshal(struct {
		Fence    lifecycle.FenceFacts
		Binding  string
		Attempts []fleet.RunnerAttempt
	}{fence, bindingValue, attempts})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func TestFleetResourceConformanceEtcd(t *testing.T) {
	s := fleetTargetConformanceStore(t)
	fleettest.RunFleetResourceConformance(t, &fleetResourceTestHarness{store: s})
}
