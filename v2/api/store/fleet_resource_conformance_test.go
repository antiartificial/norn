package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/fleettest"
	"norn/v2/api/fleet/lifecycle"
	"norn/v2/api/model"
)

// fleetResourceTestHarness implements fleettest.ResourceHarness against a
// real PG control schema. It reuses fleetTargetConformanceDB's own-schema
// setup (store/fleet_target_conformance_test.go) since resources share the
// same fleet_targets/fleet_target_fences tables.
type fleetResourceTestHarness struct {
	db *DB
	// holderTargetID/holderPlanID remember SeedHolderBinding's rows so
	// SnapshotHolderBinding and AddHolderAttempt can address them later.
	holderTargetID, holderPlanID string
}

func (h *fleetResourceTestHarness) NewResource(ctx context.Context) (string, error) {
	id := uuid.NewString()
	identity := lifecycle.TargetIdentity{Provider: "aws", ProviderAccount: "resource-" + id, StateBackend: "s3://resource-conformance-" + id + "/state"}
	target, err := h.db.RegisterFleetTarget(ctx, identity, []string{"cluster:resource-" + id}, "op-resource-"+id)
	if err != nil {
		return "", err
	}
	name := "resource-" + id
	if _, err := h.db.EnsureFleetResource(ctx, name, target.TargetID); err != nil {
		return "", err
	}
	return name, nil
}

func (h *fleetResourceTestHarness) SetDesired(ctx context.Context, name string, next controller.DesiredRevision) (*controller.Resource, error) {
	return h.db.SetDesiredFleetResource(ctx, name, next)
}

func (h *fleetResourceTestHarness) IsDesiredInvalid(err error) bool {
	return errors.Is(err, ErrFleetResourceDesiredInvalid)
}

func (h *fleetResourceTestHarness) GetResource(ctx context.Context, name string) (*controller.Resource, error) {
	return h.db.GetFleetResource(ctx, name)
}

func (h *fleetResourceTestHarness) AppendObservation(ctx context.Context, name, source string, observedAt time.Time, facts controller.ObservationFacts, evidenceRefs []string, reporter string) (*controller.Resource, controller.Observation, error) {
	return h.db.AppendFleetObservation(ctx, name, source, observedAt, facts, evidenceRefs, reporter)
}

func (h *fleetResourceTestHarness) IsObservationOutOfBounds(err error) bool {
	return errors.Is(err, ErrFleetObservationOutOfBounds)
}

func (h *fleetResourceTestHarness) ListObservations(ctx context.Context, name string, limit int) ([]controller.Observation, error) {
	return h.db.ListFleetObservations(ctx, name, limit)
}

func (h *fleetResourceTestHarness) Reconcile(ctx context.Context, name string, derive func(controller.Input) controller.Status, afterRead func()) (*controller.Resource, error) {
	return h.db.ReconcileFleetResource(ctx, name, derive, afterRead)
}

func (h *fleetResourceTestHarness) Serializes() bool { return true }

func (h *fleetResourceTestHarness) Epoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := h.db.Pool.QueryRow(ctx, `SELECT epoch FROM fleet_authority_epoch WHERE singleton`).Scan(&epoch)
	return epoch, err
}

func (h *fleetResourceTestHarness) AdvanceEpoch(ctx context.Context, expected int64, reason string) (int64, error) {
	return h.db.AdvanceFleetAuthorityEpoch(ctx, expected, reason)
}

func (h *fleetResourceTestHarness) SeedHolderBinding(ctx context.Context, name string) (string, error) {
	resource, err := h.db.GetFleetResource(ctx, name)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	planID := "plan-" + id
	if _, err := h.db.Pool.Exec(ctx, `
		UPDATE fleet_target_fences SET generation=generation+1, held=true, holder_plan_id=$2, holder_nonce_sha256=$3, revision=revision+1,
		       authority_epoch=(SELECT epoch FROM fleet_authority_epoch WHERE singleton)
		WHERE target_id=$1
	`, resource.TargetID, planID, "nonce-"+id); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	if err := h.db.InsertCompletedOperation(ctx, &model.Operation{
		ID: planID, Kind: "fleet.capacity-plan", Ref: "app", Status: model.OperationSucceeded,
		// The plan's cluster is the resource's registered cluster alias
		// (NewResource registers "cluster:"+name), so the attempt bind below
		// resolves the same target whose fence this fixture holds.
		Payload: map[string]interface{}{"cluster": name}, StartedAt: now, UpdatedAt: now, FinishedAt: &now,
	}); err != nil {
		return "", err
	}
	if _, err := h.db.CreateFleetGitHubDispatch(ctx, FleetGitHubDispatch{
		PlanID: planID, PlanRunID: 1, PlanSHA256: "sha-" + id, ApprovedHeadSHA: "head-" + id,
		DispatchNonceSHA256: "nonce-" + id, DispatchState: "dispatched",
	}); err != nil {
		return "", err
	}
	attempt := &model.FleetRunnerAttempt{
		ID: "attempt-" + id, PlanID: planID, RunnerAttemptID: "runner-" + id, Status: model.FleetRunnerAttemptRunning,
		CurrentPhase: "infrastructure_applied", HeartbeatTimeoutSeconds: 600, StartedAt: now, HeartbeatAt: now, UpdatedAt: now,
	}
	if err := h.db.CreateFleetRunnerAttempt(ctx, attempt); err != nil {
		return "", err
	}
	h.holderTargetID, h.holderPlanID = resource.TargetID, planID
	return h.SnapshotHolderBinding(ctx)
}

func (h *fleetResourceTestHarness) AddHolderAttempt(ctx context.Context) error {
	now := time.Now().UTC()
	id := uuid.NewString()
	// Terminal, because PG allows only one live attempt per plan.
	return h.db.CreateFleetRunnerAttempt(ctx, &model.FleetRunnerAttempt{
		ID: "attempt-" + id, PlanID: h.holderPlanID, RunnerAttemptID: "runner-" + id, Status: model.FleetRunnerAttemptFailed,
		CurrentPhase: "infrastructure_applied", HeartbeatTimeoutSeconds: 600, StartedAt: now, HeartbeatAt: now, UpdatedAt: now, FinishedAt: &now,
	})
}

func (h *fleetResourceTestHarness) SnapshotHolderBinding(ctx context.Context) (string, error) {
	tx, err := h.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	fence, err := GetFleetTargetFence(ctx, tx, h.holderTargetID, false)
	if err != nil {
		return "", err
	}
	dispatch, err := h.db.GetFleetGitHubDispatch(ctx, h.holderPlanID)
	if err != nil {
		return "", err
	}
	attempts, err := h.db.ListFleetRunnerAttempts(ctx, h.holderPlanID, 10)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(struct {
		Fence    lifecycle.FenceFacts
		Dispatch *FleetGitHubDispatch
		Attempts []model.FleetRunnerAttempt
	}{fence, dispatch, attempts})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func TestFleetResourceConformancePostgres(t *testing.T) {
	db := fleetTargetConformanceDB(t)
	fleettest.RunFleetResourceConformance(t, &fleetResourceTestHarness{db: db})
}
