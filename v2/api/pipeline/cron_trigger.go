package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadCronTriggerStage = "app.cron-trigger.nomad"

type cronTriggerNomad interface {
	PeriodicJobSchedule(string) (*nomad.PeriodicJobInfo, error)
	PeriodicForce(string) (string, error)
	PeriodicForceEvaluation(context.Context, string, string) (string, error)
}

// CronTriggerEffects reserves the effect before Nomad's non-idempotent Force.
// A reserved record is never dispatched a second time: an unacknowledged
// response requires operator review even if the parent has no visible child.
type CronTriggerEffects struct {
	store  *store.PGEffectStore
	client cronTriggerNomad
}

func NewCronTriggerEffects(db *store.DB, client *nomad.Client) (*CronTriggerEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad cron trigger is unavailable")
	}
	es, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	return &CronTriggerEffects{store: es, client: client}, nil
}

func (p *Pipeline) CronTriggerAvailable() bool {
	if p == nil || p.DB == nil || p.CronTriggerEffects == nil || p.CronTriggerEffects.store == nil || p.CronTriggerEffects.client == nil {
		return false
	}
	_, ok := p.OperationStore.(interface {
		VerifyAcceptedOperation(context.Context, string) (store.AcceptedOperation, error)
	})
	return ok
}

type cronTriggerRequest struct {
	App         string `json:"app"`
	Process     string `json:"process"`
	JobID       string `json:"jobId"`
	Schedule    string `json:"schedule"`
	TimeZone    string `json:"timezone"`
	SpecDigest  string `json:"specDigest"`
	Version     uint64 `json:"version"`
	ModifyIndex uint64 `json:"modifyIndex"`
}

func cronTriggerRequestFromOperation(op *model.Operation) (cronTriggerRequest, error) {
	if op == nil {
		return cronTriggerRequest{}, fmt.Errorf("cron trigger operation is required")
	}
	q := cronTriggerRequest{App: op.App, Process: stringFromMap(op.Payload, "process"), JobID: stringFromMap(op.Payload, "jobId"), Schedule: stringFromMap(op.Payload, "schedule"), TimeZone: stringFromMap(op.Payload, "timezone"), SpecDigest: stringFromMap(op.Payload, "specDigest"), Version: uint64FromMap(op.Payload, "version"), ModifyIndex: uint64FromMap(op.Payload, "modifyIndex")}
	if q.App == "" || q.Process == "" || q.JobID != q.App+"-"+q.Process || q.Schedule == "" || !strings.HasPrefix(q.SpecDigest, "sha256:") || len(q.SpecDigest) != 71 || q.ModifyIndex == 0 {
		return cronTriggerRequest{}, fmt.Errorf("cron trigger descriptor is invalid")
	}
	return q, nil
}

func (p *Pipeline) executeCronTrigger(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CronTriggerAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable cron trigger execution is unavailable"}
	}
	q, err := cronTriggerRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	verifier, ok := p.OperationStore.(interface {
		VerifyAcceptedOperation(context.Context, string) (store.AcceptedOperation, error)
	})
	if !ok {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "signed cron trigger acceptance is unavailable"}
	}
	accepted, err := verifier.VerifyAcceptedOperation(ctx, op.ID)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "signed cron trigger acceptance failed verification"}
	}
	signed, err := cronTriggerRequestFromOperation(&accepted.Operation)
	if err != nil || accepted.Operation.Kind != op.Kind || accepted.Operation.ID != op.ID || signed != q {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron trigger claim differs from signed acceptance"}
	}
	resource := "app/" + q.App + "/cron/" + q.Process
	if err := p.DB.CheckOperationClaim(ctx, claim); err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "cron trigger claim is no longer current", Cause: err})
	}
	if previous, found, err := p.CronTriggerEffects.store.LatestForOperation(ctx, op.ID, nomadCronTriggerStage); err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "read prior cron trigger effect", Cause: err})
	} else if found {
		var stored cronTriggerRequest
		if json.Unmarshal(previous.Reservation.LaunchPayload, &stored) != nil || stored != q {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron trigger reservation differs from signed intent"}
		}
		return p.recoverCronTrigger(ctx, claim, q, previous)
	}
	// Only the signed parent revision may be forced. Nomad has no CAS option for
	// Force, so the second read in dispatch narrows but cannot eliminate a race.
	spec, err := p.findSpec(q.App)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "scheduled app is unavailable"}
	}
	digest, err := model.InfraSpecDigest(spec)
	proc, exists := spec.Processes[q.Process]
	if err != nil || digest != q.SpecDigest || !exists || proc.Schedule == "" || model.ResolveProcessTimezone(spec, proc) != q.TimeZone {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "scheduled process changed since acceptance"}
	}
	state, err := p.CronTriggerEffects.client.PeriodicJobSchedule(q.JobID)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "read periodic parent", Cause: err})
	}
	if state.Paused || !cronPauseIntentMatches(cronPauseRequest{JobID: q.JobID, Schedule: q.Schedule, TimeZone: q.TimeZone, Version: q.Version, ModifyIndex: q.ModifyIndex}, state, true) {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "periodic parent no longer matches accepted trigger"}
	}
	authority, err := p.CronTriggerEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "control authority unavailable", Cause: err})
	}
	payload, _ := json.Marshal(q)
	reservation := effect.Reservation{Authority: authority, Resource: resource, OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: nomadCronTriggerStage, Supervisor: "nomad-cron-trigger", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint cron trigger intent: " + err.Error()}
	}
	sum := sha256.Sum256([]byte(authority + "\x00" + op.ID + "\x00" + reservation.InputDigest))
	reservation.SupervisorExecutionID = "nomad-cron-trigger-" + hex.EncodeToString(sum[:16])
	reserved, err := p.CronTriggerEffects.store.Reserve(ctx, reservation)
	if errors.Is(err, effect.ErrResourceBlocked) {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "another mutable app effect is unresolved", Cause: err})
	}
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: resource, Reason: "reserve cron trigger effect", Cause: err})
	}
	if !reserved.Created {
		return p.recoverCronTrigger(ctx, claim, q, reserved.Record)
	}
	// No path after this point calls Force again for this operation. A failed
	// transport request is ambiguous because Nomad may have accepted the child.
	state, err = p.CronTriggerEffects.client.PeriodicJobSchedule(q.JobID)
	if err != nil || state.Paused || !cronPauseIntentMatches(cronPauseRequest{JobID: q.JobID, Schedule: q.Schedule, TimeZone: q.TimeZone, Version: q.Version, ModifyIndex: q.ModifyIndex}, state, true) {
		return deferredResult(claim, &effect.PendingError{EffectID: reserved.Record.Token.EffectID, Resource: resource, Reason: "periodic parent changed after effect reservation; operator review required", Cause: err})
	}
	evalID, err := p.CronTriggerEffects.client.PeriodicForce(q.JobID)
	if err != nil || evalID == "" {
		return deferredResult(claim, &effect.PendingError{EffectID: reserved.Record.Token.EffectID, Resource: resource, Reason: "Nomad cron trigger response is ambiguous; operator review required", Cause: err})
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: evalID}
	if err := p.CronTriggerEffects.store.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: reserved.Record.Token.EffectID, Resource: resource, Reason: "cron trigger evaluation acknowledgement was not durable; operator review required", Cause: err})
	}
	reserved.Record.Execution = identity
	reserved.Record.Lifecycle = effect.LifecycleLaunched
	return p.recoverCronTrigger(ctx, claim, q, reserved.Record)
}

func (p *Pipeline) recoverCronTrigger(ctx context.Context, claim store.OperationClaim, q cronTriggerRequest, record effect.Record) *OperationResult {
	resource := "app/" + q.App + "/cron/" + q.Process
	if record.Lifecycle == effect.LifecycleReserved || record.Execution.RuntimeInstanceID == "" {
		return deferredResult(claim, &effect.PendingError{EffectID: record.Token.EffectID, Resource: resource, Reason: "cron trigger dispatch outcome is ambiguous; operator review required"})
	}
	evalID := record.Execution.RuntimeInstanceID
	childID, err := p.CronTriggerEffects.client.PeriodicForceEvaluation(ctx, evalID, q.JobID)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: record.Token.EffectID, Resource: resource, Reason: "verify cron trigger evaluation", Cause: err})
	}
	output, _ := json.Marshal(map[string]string{"evalId": evalID, "jobId": childID})
	if record.Lifecycle == effect.LifecycleLaunched {
		verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest, ResultDigest: effect.DigestInput(output), ResultReference: evalID, SupervisorExecutionID: record.Reservation.SupervisorExecutionID, RuntimeInstanceID: evalID, EvidenceSource: "nomad.evaluation", EvidenceReference: evalID, ObservedAt: time.Now().UTC()}
		if err := p.CronTriggerEffects.store.Complete(ctx, record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
			return deferredResult(claim, &effect.PendingError{EffectID: record.Token.EffectID, Resource: resource, Reason: "record cron trigger evaluation", Cause: err})
		}
	} else if record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeSucceeded || record.Completion.Verification.ResultDigest != effect.DigestInput(output) {
		return deferredResult(claim, &effect.PendingError{EffectID: record.Token.EffectID, Resource: resource, Reason: "cron trigger completion evidence differs from Nomad"})
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: fmt.Sprintf("%s cron process %q triggered", q.App, q.Process), Metadata: map[string]interface{}{"process": q.Process, "jobId": childID, "evalId": evalID, "effectId": record.Token.EffectID}}
}
