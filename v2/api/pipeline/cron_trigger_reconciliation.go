package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type cronTriggerSourceVerifier interface {
	VerifyAcceptedOperation(context.Context, string) (store.AcceptedOperation, error)
}

func (p *Pipeline) cronTriggerReconciliationSource(ctx context.Context, app, sourceID, effectID string) (cronTriggerRequest, effect.Record, error) {
	if !p.CronTriggerAvailable() {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	verifier, ok := p.OperationStore.(cronTriggerSourceVerifier)
	if !ok {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	accepted, err := verifier.VerifyAcceptedOperation(ctx, sourceID)
	if err != nil {
		return cronTriggerRequest{}, effect.Record{}, err
	}
	source := accepted.Operation
	q, err := cronTriggerRequestFromOperation(&source)
	if err != nil || source.Kind != "app.cron-trigger" || source.App != app || source.Status != model.OperationFailed || source.Metadata["manualRecoveryRequired"] != true || source.Metadata["externalEffectRecoveryPending"] != true {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	record, found, err := p.CronTriggerEffects.store.LatestForOperation(ctx, sourceID, nomadCronTriggerStage)
	if err != nil {
		return cronTriggerRequest{}, effect.Record{}, err
	}
	if !found || record.Token.EffectID != effectID || (record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched && record.Lifecycle != effect.LifecycleCompleted) || record.Reservation.Resource != "app/"+app+"/cron/"+q.Process {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	var corrected bool
	if err := p.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE kind='app.cron-trigger-reconcile' AND status='succeeded' AND payload->>'sourceOperationId'=$1)`, sourceID).Scan(&corrected); err != nil || corrected {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	var stored cronTriggerRequest
	if json.Unmarshal(record.Reservation.LaunchPayload, &stored) != nil || stored != q {
		return cronTriggerRequest{}, effect.Record{}, store.ErrCronTriggerReconciliationUnavailable
	}
	return q, record, nil
}

// QueueCronTriggerReconciliation records an operator's positive assertion that
// a specific Nomad evaluation is the missing Force result. The worker repeats
// every check before the atomic correction; this admission writes no effect.
func (p *Pipeline) QueueCronTriggerReconciliation(ctx context.Context, app, sourceID, effectID, evalID string, request EnqueueRequest) (store.AcceptedOperation, error) {
	q, record, err := p.cronTriggerReconciliationSource(ctx, app, sourceID, effectID)
	if err != nil {
		return store.AcceptedOperation{}, err
	}
	if evalID == "" || (record.Execution.RuntimeInstanceID != "" && record.Execution.RuntimeInstanceID != evalID) {
		return store.AcceptedOperation{}, store.ErrCronTriggerReconciliationUnavailable
	}
	if _, err := p.CronTriggerEffects.client.PeriodicForceEvaluation(ctx, evalID, q.JobID); err != nil {
		return store.AcceptedOperation{}, fmt.Errorf("Nomad cron trigger evaluation is unproven: %w", err)
	}
	now := time.Now().UTC()
	request.Admission.OneActiveMutablePerApp = true
	op := model.Operation{ID: uuid.NewString(), Kind: "app.cron-trigger-reconcile", App: app, SagaID: uuid.NewString(), Ref: sourceID, Status: model.OperationQueued,
		Risk: "reconcile ambiguous Nomad cron trigger", Source: "app-control-api", Message: "queued cron trigger reconciliation", StartedAt: now, NextAttemptAt: now, MaxAttempts: 3,
		Payload: map[string]interface{}{"sourceOperationId": sourceID, "effectId": effectID, "evalId": evalID, "jobId": q.JobID}}
	request.Semantics = op.Payload
	return p.QueueOperation(ctx, op, request)
}

func (p *Pipeline) executeCronTriggerReconciliation(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CronTriggerAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron trigger reconciliation is unavailable"}
	}
	verifier, ok := p.OperationStore.(cronTriggerSourceVerifier)
	if !ok {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "signed cron trigger reconciliation is unavailable"}
	}
	accepted, err := verifier.VerifyAcceptedOperation(ctx, op.ID)
	if err != nil || accepted.Operation.Kind != op.Kind || accepted.Operation.ID != op.ID || accepted.Operation.Status != model.OperationRunning {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "signed cron trigger reconciliation is unavailable"}
	}
	sourceID, effectID, evalID := stringFromMap(op.Payload, "sourceOperationId"), stringFromMap(op.Payload, "effectId"), stringFromMap(op.Payload, "evalId")
	if sourceID == "" || effectID == "" || evalID == "" || op.Ref != sourceID || stringFromMap(accepted.Operation.Payload, "effectId") != effectID || stringFromMap(accepted.Operation.Payload, "evalId") != evalID {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron trigger reconciliation intent is invalid"}
	}
	q, record, err := p.cronTriggerReconciliationSource(ctx, op.App, sourceID, effectID)
	if err != nil || stringFromMap(op.Payload, "jobId") != q.JobID || (record.Execution.RuntimeInstanceID != "" && record.Execution.RuntimeInstanceID != evalID) {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "source cron trigger effect is unavailable"}
	}
	childID, err := p.CronTriggerEffects.client.PeriodicForceEvaluation(ctx, evalID, q.JobID)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: effectID, Resource: record.Reservation.Resource, Reason: "verify operator supplied Nomad evaluation", Cause: err})
	}
	observedAt := time.Now().UTC()
	if err := p.DB.CompleteCronTriggerReconciliation(ctx, claim, op.App, sourceID, effectID, q.JobID, evalID, childID, observedAt); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron trigger correction could not commit: " + err.Error()}
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: "cron trigger reconciled against Nomad evaluation",
		Metadata: map[string]interface{}{"sourceOperationId": sourceID, "effectId": effectID, "evalId": evalID, "jobId": childID}, finished: true}
}
