package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"norn/v2/api/effect"
)

var ErrCronTriggerReconciliationUnavailable = errors.New("cron trigger reconciliation is unavailable")

// CompleteCronTriggerReconciliation releases one ambiguous Force reservation
// and records the separate correction receipt in the same transaction. The
// original failed operation and its manual-review receipt are never changed.
func (db *DB) CompleteCronTriggerReconciliation(ctx context.Context, claim OperationClaim, app, sourceID, effectID, parentJobID, evalID, childJobID string, observedAt time.Time) error {
	if db == nil || db.Pool == nil || !claim.valid() || app == "" || sourceID == "" || effectID == "" || parentJobID == "" || evalID == "" || childJobID == "" || observedAt.IsZero() || observedAt.After(time.Now().Add(time.Minute)) {
		return ErrCronTriggerReconciliationUnavailable
	}
	output, err := json.Marshal(map[string]string{"evalId": evalID, "jobId": childJobID})
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]interface{}{"sourceOperationId": sourceID, "effectId": effectID, "evalId": evalID, "jobId": childJobID, "observedAt": observedAt.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// One Nomad evaluation cannot be credited to two ambiguous reservations.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "norn:cron-trigger-eval:"+evalID); err != nil {
		return err
	}
	var correctionSaga string
	if err := tx.QueryRow(ctx, `SELECT saga_id FROM operations WHERE id=$1 AND kind='app.cron-trigger-reconcile' AND app=$2
		AND ref=$3 AND payload->>'sourceOperationId'=$3 AND payload->>'effectId'=$4 AND payload->>'evalId'=$5
		AND acceptance_required AND EXISTS(SELECT 1 FROM operation_acceptance_intents ai WHERE ai.operation_id=operations.id)
		AND status='running' AND locked_by=$6 AND lock_generation=$7 AND locked_until>clock_timestamp() FOR UPDATE`,
		claim.OperationID(), app, sourceID, effectID, evalID, claim.OwnerID(), claim.Generation()).Scan(&correctionSaga); err != nil {
		return ErrOperationOwnershipLost
	}
	var sourceSaga, sourceProcess, sourceJob, sourceStatus string
	var manual, pending bool
	if err := tx.QueryRow(ctx, `SELECT saga_id,ref,payload->>'jobId',status,
		COALESCE(metadata->>'manualRecoveryRequired'='true',false),
		COALESCE(metadata->>'externalEffectRecoveryPending'='true',false)
		FROM operations WHERE id=$1 AND app=$2 AND kind='app.cron-trigger' FOR UPDATE`, sourceID, app).
		Scan(&sourceSaga, &sourceProcess, &sourceJob, &sourceStatus, &manual, &pending); err != nil {
		return ErrCronTriggerReconciliationUnavailable
	}
	if sourceStatus != "failed" || !manual || !pending || sourceJob != parentJobID || sourceProcess == "" || correctionSaga == "" || sourceSaga == "" {
		return ErrCronTriggerReconciliationUnavailable
	}
	var archived bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM evidence_archive_intents WHERE operation_id=$1 AND subject_kind='saga' AND subject_id=$2)`, sourceID, sourceSaga).Scan(&archived); err != nil || !archived {
		return ErrCronTriggerReconciliationUnavailable
	}
	var duplicate bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operation_effects WHERE id<>$1 AND runtime_instance_id=$2 AND lifecycle='completed')
		OR EXISTS(SELECT 1 FROM operations WHERE id<>$3 AND kind='app.cron-trigger-reconcile' AND status='succeeded'
		AND (payload->>'sourceOperationId'=$4 OR payload->>'evalId'=$2))`, effectID, evalID, claim.OperationID(), sourceID).Scan(&duplicate); err != nil || duplicate {
		return ErrCronTriggerReconciliationUnavailable
	}
	var generation int64
	var authority, inputDigest, supervisor, executionID, lifecycle, runtimeID, outcome, resultDigest, evidenceSource, evidenceReference string
	var launchPayload []byte
	if err := tx.QueryRow(ctx, `SELECT generation,authority::text,input_digest,supervisor,supervisor_execution_id,lifecycle,runtime_instance_id,launch_payload,
		COALESCE(outcome,''),COALESCE(result_digest,''),COALESCE(evidence_source,''),COALESCE(evidence_reference,'')
		FROM operation_effects WHERE id=$1 AND operation_id=$2 AND stage='app.cron-trigger.nomad'
		AND resource=$3 FOR UPDATE`, effectID, sourceID, "app/"+app+"/cron/"+sourceProcess).
		Scan(&generation, &authority, &inputDigest, &supervisor, &executionID, &lifecycle, &runtimeID, &launchPayload,
			&outcome, &resultDigest, &evidenceSource, &evidenceReference); err != nil {
		return ErrCronTriggerReconciliationUnavailable
	}
	if supervisor != "nomad-cron-trigger" || executionID == "" || (lifecycle != "reserved" && lifecycle != "launched" && lifecycle != "completed") || (runtimeID != "" && runtimeID != evalID) {
		return ErrCronTriggerReconciliationUnavailable
	}
	if lifecycle == "completed" && (outcome != "succeeded" || resultDigest != effect.DigestInput(output) || evidenceSource != "nomad.evaluation" || evidenceReference != evalID) {
		return ErrCronTriggerReconciliationUnavailable
	}
	var stored struct {
		App     string `json:"app"`
		Process string `json:"process"`
		JobID   string `json:"jobId"`
	}
	if json.Unmarshal(launchPayload, &stored) != nil || stored.App != app || stored.Process != sourceProcess || stored.JobID != parentJobID {
		return ErrCronTriggerReconciliationUnavailable
	}
	var owner, opID string
	var claimGeneration int64
	if err := tx.QueryRow(ctx, `SELECT operation_id,claim_owner,claim_generation FROM operation_effects WHERE id=$1`, effectID).Scan(&opID, &owner, &claimGeneration); err != nil || opID != sourceID || owner == "" || claimGeneration <= 0 {
		return ErrCronTriggerReconciliationUnavailable
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/" + app + "/cron/" + sourceProcess,
		OperationClaim: effect.OperationClaim{OperationID: sourceID, OwnerID: owner, Generation: claimGeneration}, Stage: "app.cron-trigger.nomad", Supervisor: supervisor, SupervisorExecutionID: executionID, LaunchPayload: launchPayload}
	computed, err := effect.ComputeInputDigest(reservation)
	if err != nil || computed != inputDigest {
		return ErrCronTriggerReconciliationUnavailable
	}
	if lifecycle != "completed" {
		result, err := tx.Exec(ctx, `UPDATE operation_effects SET lifecycle='completed',outcome='succeeded',runtime_instance_id=$2,
		result_digest=$3,result_reference=$2,evidence_source='nomad.evaluation',evidence_reference=$2,
		evidence_observed_at=$4,completed_at=now(),updated_at=now()
		WHERE id=$1 AND generation=$5 AND lifecycle IN ('reserved','launched')`, effectID, evalID, effect.DigestInput(output), observedAt, generation)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrCronTriggerReconciliationUnavailable
		}
	}
	var finished int
	if err := tx.QueryRow(ctx, `WITH finished AS (
		UPDATE operations SET status='succeeded',message='cron trigger reconciled against Nomad evaluation',
		metadata=metadata || $2::jsonb,locked_by='',locked_until=NULL,updated_at=now(),finished_at=now()
		WHERE id=$1 AND kind='app.cron-trigger-reconcile' AND status='running' AND locked_by=$3
		AND lock_generation=$4 AND locked_until>clock_timestamp()
		RETURNING id,saga_id,app
	),outbox AS (
		INSERT INTO evidence_archive_intents (id,subject_kind,subject_id,app,operation_id,sequence,state)
		SELECT 'ei-' || gen_random_uuid()::text,'saga',saga_id,app,id,1,'pending' FROM finished WHERE saga_id<>''
		ON CONFLICT (subject_kind,subject_id,sequence) DO NOTHING
	) SELECT count(*) FROM finished`, claim.OperationID(), metadata, claim.OwnerID(), claim.Generation()).Scan(&finished); err != nil {
		return err
	}
	if finished != 1 {
		return fmt.Errorf("%w: correction claim expired", ErrOperationOwnershipLost)
	}
	return tx.Commit(ctx)
}
