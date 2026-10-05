package store

// Atomic fleet.resource.execution-mode on PostgreSQL (plan-5-7, hand-offs from
// the E4b review). The mode is applied and the signed operation recorded in
// ONE transaction, through the ordinary acceptance path:
//
//  1. the request identity is inserted first, so a replay returns the stored
//     operation and applies nothing, and the same key with another body is
//     idempotency_key_reused without applying anything (acceptWithGuard);
//  2. the resource row is locked FOR UPDATE, then the target fence FOR UPDATE;
//     a held fence (either lane) refuses with
//     executor.CodeFleetExecutionModeFenceHeld, which rolls the identity back
//     too, so a refusal leaves no ledger row;
//  3. expectedRevision is a compare-and-set on the resource revision;
//  4. executionMode is written into the resource document and the revision is
//     bumped only when the effective mode changes;
//  5. on a switch to github, every converge plan of the resource that has no
//     attempt is abandoned in the same transaction.
//
// Lock order. The hand-off lists fence then resource, but ReconcileFleetResource
// locks the resource row FOR UPDATE first and then the fence FOR SHARE, so a
// fence-first order here would deadlock against a reconcile. The resource is
// locked first (a resource's target is fixed at creation); the observable
// decisions keep the specified order: a held fence is refused before the
// revision compare. The plan advisory locks of step 5 are taken before the
// fence, as every other fence writer does (plan locks, then fence). The mode
// is never read by an execution transaction (T7).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/fleet/controller"
	"norn/v2/api/fleet/executor"
	"norn/v2/api/model"
)

// FleetResourceExecutionModeAdmission is the typed admission of
// fleet.resource.execution-mode. It is the only part of the change in the
// request fingerprint; the previous mode is server-gathered and recorded in
// operation metadata after the fingerprint was sealed.
type FleetResourceExecutionModeAdmission struct {
	Resource         string `json:"resource"`
	Mode             string `json:"mode"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

// FleetResourceExecutionModePreviousModeKey is the operation-metadata key
// holding the effective mode before the change.
const FleetResourceExecutionModePreviousModeKey = "previousMode"

// NormalizeFleetResourceExecutionModeAcceptance validates the typed admission
// against the operation identity. Backends other than PostgreSQL call it too.
func NormalizeFleetResourceExecutionModeAcceptance(acceptance *OperationAcceptance) error {
	admission := acceptance.FleetResourceExecutionMode
	if admission == nil {
		if acceptance.Operation.Kind == executor.KindResourceExecutionMode || acceptance.Identity.Kind == executor.KindResourceExecutionMode {
			return &AcceptanceValidationError{Reason: "fleet resource execution mode operations require typed admission"}
		}
		return nil
	}
	if acceptance.FleetReconciliation != nil || acceptance.FleetRunnerAttempt != nil || acceptance.FleetTargetMutation != nil || acceptance.Deployment != nil {
		return &AcceptanceValidationError{Reason: "fleet resource execution mode cannot combine with another typed admission"}
	}
	admission.Resource = strings.TrimSpace(admission.Resource)
	if !controller.ValidResourceName(admission.Resource) {
		return &AcceptanceValidationError{Reason: "fleet resource execution mode requires a valid resource name"}
	}
	if err := (executor.ExecutionModeRequest{Mode: admission.Mode, ExpectedRevision: admission.ExpectedRevision}).Validate(); err != nil {
		return &AcceptanceValidationError{Reason: "fleet resource execution mode: " + err.Error()}
	}
	if acceptance.Operation.Kind != executor.KindResourceExecutionMode || acceptance.Identity.Kind != executor.KindResourceExecutionMode ||
		acceptance.Identity.Resource != admission.Resource || acceptance.Operation.Ref != admission.Resource {
		return &AcceptanceValidationError{Reason: "fleet resource execution mode admission must match operation identity"}
	}
	if acceptance.Operation.Status != model.OperationSucceeded {
		return &AcceptanceValidationError{Reason: "fleet resource execution mode acceptance operation must be a completed receipt"}
	}
	return nil
}

// newFleetResourceExecutionModeAcceptance builds the signed acceptance for one
// request. identity carries authority, actor, kind, resource and key.
func newFleetResourceExecutionModeAcceptance(identity OperationRequestIdentity, audit AcceptanceAuditContext, request executor.ExecutionModeRequest) (OperationAcceptance, error) {
	if err := request.Validate(); err != nil {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "fleet resource execution mode: " + err.Error()}
	}
	if identity.Kind != executor.KindResourceExecutionMode || identity.Resource == "" {
		return OperationAcceptance{}, &AcceptanceValidationError{Reason: "fleet resource execution mode identity is invalid"}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	finished := now
	acceptance := OperationAcceptance{
		Identity: identity, Audit: audit,
		FleetResourceExecutionMode: &FleetResourceExecutionModeAdmission{Resource: identity.Resource, Mode: request.Mode, ExpectedRevision: request.ExpectedRevision},
		Operation: model.Operation{
			ID: uuid.NewString(), Kind: identity.Kind, Ref: identity.Resource, Status: model.OperationSucceeded, MaxAttempts: 1,
			Risk: "changes which lane executes the resource; no provider mutation performed", Source: "control-api",
			Message:   "fleet resource execution mode recorded",
			Payload:   map[string]interface{}{"resource": identity.Resource, "mode": request.Mode},
			Metadata:  map[string]interface{}{"resource": identity.Resource, "mode": request.Mode},
			StartedAt: now, UpdatedAt: now, FinishedAt: &finished,
		},
	}
	if err := NormalizeFleetResourceExecutionModeAcceptance(&acceptance); err != nil {
		return OperationAcceptance{}, err
	}
	fingerprint, err := CanonicalOperationRequestFingerprint(acceptance)
	if err != nil {
		return OperationAcceptance{}, err
	}
	acceptance.Fingerprint = fingerprint
	return acceptance, nil
}

// SetFleetResourceExecutionMode applies request to identity.Resource and
// records the signed fleet.resource.execution-mode operation atomically (see
// the file comment). Refusals: *executor.Error with
// executor.CodeFleetExecutionModeFenceHeld, ErrFleetResourceNotFound,
// ErrFleetResourceRevisionConflict, and the acceptance errors (replay,
// idempotency_key_reused).
func (s *PGOperationStore) SetFleetResourceExecutionMode(ctx context.Context, identity OperationRequestIdentity, audit AcceptanceAuditContext, request executor.ExecutionModeRequest) (AcceptedOperation, error) {
	acceptance, err := newFleetResourceExecutionModeAcceptance(identity, audit, request)
	if err != nil {
		return AcceptedOperation{}, err
	}
	return s.acceptWithGuard(ctx, acceptance, nil, nil)
}

// enforceFleetResourceExecutionMode is the in-transaction half, called by
// acceptWithGuard after the request identity was inserted. It records the
// previous mode in the operation metadata (outside the fingerprint, which was
// sealed before the transaction; metadata may only grow).
func enforceFleetResourceExecutionMode(ctx context.Context, tx pgx.Tx, acceptance *OperationAcceptance) error {
	admission := acceptance.FleetResourceExecutionMode
	resource, err := scanFleetResource(tx.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1 FOR UPDATE`, admission.Resource))
	if err == pgx.ErrNoRows {
		return ErrFleetResourceNotFound
	}
	if err != nil {
		return err
	}
	changing := controller.EffectiveExecutionMode(resource.ExecutionMode) != admission.Mode
	var planIDs []string
	if changing && admission.Mode == executor.ModeGitHub {
		if planIDs, err = unstartedConvergePlansTx(ctx, tx, admission.Resource); err != nil {
			return err
		}
		for _, planID := range planIDs {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:fleet-attempt:"+planID); err != nil {
				return err
			}
		}
	}
	fence, err := GetFleetTargetFence(ctx, tx, resource.TargetID, true)
	if err != nil {
		return err
	}
	if fence.Held {
		return executorRefusal(executor.CodeFleetExecutionModeFenceHeld, "the target's fence is held by plan %q; wait for it to finish or release it before changing the execution mode", fence.HolderPlanID)
	}
	if resource.Revision != admission.ExpectedRevision {
		return ErrFleetResourceRevisionConflict
	}
	previous := controller.EffectiveExecutionMode(resource.ExecutionMode)
	if changing {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		now = now.UTC().Truncate(time.Microsecond)
		resource.ExecutionMode = admission.Mode
		resource.UpdatedAt = now
		document, err := json.Marshal(fleetResourceDocumentOf(*resource))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE fleet_resources SET document=$2, revision=revision+1, updated_at=$3 WHERE name=$1`, admission.Resource, document, now); err != nil {
			return err
		}
		// Every unstarted converge plan is abandoned: start already refuses an
		// abandoned plan and never reads the mode (T7).
		for _, planID := range planIDs {
			if err := abandonFleetPlanTx(ctx, tx, planID, nil, acceptance.Operation.ID, now); err != nil {
				return err
			}
		}
	}
	metadata := make(map[string]interface{}, len(acceptance.Operation.Metadata)+1)
	for key, value := range acceptance.Operation.Metadata {
		metadata[key] = value
	}
	metadata[FleetResourceExecutionModePreviousModeKey] = previous
	acceptance.Operation.Metadata = metadata
	return nil
}

// enforceFleetResourceModeRequired is the recording-path gate behind
// OperationAcceptance.RequireFleetResourceMode. It takes the resource row FOR
// SHARE first (the lock order of the mode switch, which locks FOR UPDATE and
// then abandons the plans it finds), so a record that passes the check
// commits before a concurrent switch to github can look for plans to
// abandon, and a switch that committed first is seen here. A resource that is
// missing or in another mode is controller.ErrNotWatched.
func enforceFleetResourceModeRequired(ctx context.Context, tx pgx.Tx, acceptance *OperationAcceptance) error {
	resource, err := scanFleetResource(tx.QueryRow(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE name=$1 FOR SHARE`, acceptance.Operation.Ref))
	if err == pgx.ErrNoRows {
		return controller.ErrNotWatched
	}
	if err != nil {
		return err
	}
	if controller.EffectiveExecutionMode(resource.ExecutionMode) != acceptance.RequireFleetResourceMode {
		return controller.ErrNotWatched
	}
	return nil
}

// unstartedConvergePlansTx lists resource's recorded converge plans that have
// no attempt and are not abandoned, oldest first.
func unstartedConvergePlansTx(ctx context.Context, tx pgx.Tx, resource string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.id FROM operations o
		WHERE o.kind='fleet.capacity-plan' AND o.ref=$1 AND o.status='succeeded'
		  AND o.payload->>'action'=$2 AND o.payload->'converge'->>'resource'=$1
		  AND NOT EXISTS (SELECT 1 FROM fleet_runner_attempts a WHERE a.plan_id=o.id)
		  AND NOT EXISTS (SELECT 1 FROM fleet_target_abandoned_plans ap WHERE ap.plan_id=o.id)
		ORDER BY o.started_at ASC, o.id ASC
	`, resource, executor.ActionConverge)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(ids)
	return ids, nil
}

// ListWatchedResources returns only the resources whose stored executionMode
// is controller, for the converge watcher (controller.WatchedResourceSource),
// in name order. Cluster is the resource target's cluster alias; LastApplied is
// status.lastApplied.commitSha, or "" when it is absent or carries a reason.
// A controller resource whose target or cluster alias is missing is skipped.
// Mode is enforced here, at plan recording, never in an execution transaction
// (T7). Same entries as etcdstore.V3OperationStore.ListWatchedResources.
func (db *DB) ListWatchedResources(ctx context.Context) ([]controller.WatchedResource, error) {
	rows, err := db.Pool.Query(ctx, `SELECT `+fleetResourceColumns+` FROM fleet_resources WHERE document->>'executionMode' = $1 ORDER BY name`, executor.ModeController)
	if err != nil {
		return nil, err
	}
	var resources []controller.Resource
	for rows.Next() {
		resource, err := scanFleetResource(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		resources = append(resources, *resource)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []controller.WatchedResource
	for _, resource := range resources {
		target, err := db.GetFleetTarget(ctx, resource.TargetID)
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
