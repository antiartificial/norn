package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadCronPauseStage = "app.cron-pause.nomad"

// NomadCronPauseEffects is the single fenced boundary for stopping a periodic
// job. A lost response is reconciled from Nomad before a successor can decide
// whether the effect completed.
type NomadCronPauseEffects struct {
	executor *effect.Executor
	store    *store.PGEffectStore
	client   *nomad.Client
}

func NewNomadCronPauseEffects(db *store.DB, client *nomad.Client) (*NomadCronPauseEffects, error) {
	if client == nil {
		return nil, fmt.Errorf("Nomad client is unavailable")
	}
	effectStore, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	e := &NomadCronPauseEffects{store: effectStore, client: client}
	e.executor = &effect.Executor{Store: effectStore, Supervisor: &nomadCronPauseSupervisor{client: client, db: db}, Verifier: nomadCronPauseVerifier{}}
	return e, nil
}
func (e *NomadCronPauseEffects) available() bool {
	return e != nil && e.store != nil && e.executor != nil && e.client != nil
}
func (p *Pipeline) CronPauseAvailable() bool { return p != nil && p.CronPauseEffects.available() }

type cronPauseRequest struct {
	App         string `json:"app"`
	Process     string `json:"process"`
	Schedule    string `json:"schedule"`
	TimeZone    string `json:"timezone"`
	JobID       string `json:"jobId"`
	Version     uint64 `json:"version"`
	ModifyIndex uint64 `json:"modifyIndex"`
}

func cronPauseRequestFromOperation(op *model.Operation) (cronPauseRequest, error) {
	if op == nil {
		return cronPauseRequest{}, fmt.Errorf("cron pause operation is required")
	}
	r := cronPauseRequest{App: op.App, Process: stringFromMap(op.Payload, "process"), Schedule: stringFromMap(op.Payload, "schedule"), TimeZone: stringFromMap(op.Payload, "timezone"), JobID: stringFromMap(op.Payload, "jobId"), Version: uint64FromMap(op.Payload, "version"), ModifyIndex: uint64FromMap(op.Payload, "modifyIndex")}
	if strings.TrimSpace(r.App) == "" || strings.TrimSpace(r.Process) == "" || strings.TrimSpace(r.Schedule) == "" || r.JobID != r.App+"-"+r.Process || r.Version == 0 || r.ModifyIndex == 0 {
		return cronPauseRequest{}, fmt.Errorf("cron pause descriptor is invalid")
	}
	return r, nil
}
func cronPauseResource(r cronPauseRequest) string { return "app/" + r.App + "/cron/" + r.Process }
func cronPauseExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-cron-pause-" + hex.EncodeToString(sum[:16])
}

func (p *Pipeline) executeCronPause(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CronPauseAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.cron-pause execution is unavailable"}
	}
	r, err := cronPauseRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	if p.DB == nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron state store is unavailable"}
	}
	if err := p.DB.CheckOperationClaim(ctx, claim); err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronPauseResource(r), Reason: "cron pause claim is no longer current", Cause: err})
	}
	authority, err := p.CronPauseEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronPauseResource(r), Reason: "control authority is unavailable", Cause: err})
	}
	// A lease can expire after Nomad accepted StopJob but before the state write.
	// Recover this operation's exact effect before treating a stopped parent as a
	// conflicting pause, otherwise the durable intent is stranded forever.
	if prior, found, lookupErr := p.CronPauseEffects.store.LatestForOperation(ctx, op.ID, nomadCronPauseStage); lookupErr != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronPauseResource(r), Reason: "prior cron pause lookup failed", Cause: lookupErr})
	} else if found {
		stored, storedErr := cronPauseRequestFromReservation(prior.Reservation)
		if storedErr != nil || stored != r {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron pause effect descriptor does not match signed operation intent"}
		}
		result, recoverErr := p.CronPauseEffects.executor.Recover(ctx, prior)
		if recoverErr != nil {
			return deferredResult(claim, recoverErr)
		}
		if result.Outcome != effect.OutcomeSucceeded {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron pause request failed"}
		}
		return p.completeCronPause(ctx, claim, r, result)
	}
	// The periodic parent Stop flag, rather than the generic job status, is the
	// proof of a pause. A periodic job may be dead because a child completed or
	// failed, which must never be credited to this pause operation.
	periodic, statusErr := p.CronPauseEffects.client.PeriodicJobSchedule(r.JobID)
	if statusErr != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronPauseResource(r), Reason: "read periodic job state", Cause: statusErr})
	}
	if periodic.Paused {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "periodic job is already stopped"}
	}
	if !cronPauseIntentMatches(r, periodic, true) {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad periodic job no longer matches cron pause intent"}
	}
	payload, _ := json.Marshal(r)
	reservation := effect.Reservation{Authority: authority, Resource: cronPauseResource(r), OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: nomadCronPauseStage, Supervisor: "nomad-cron-pause", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint cron pause request: " + err.Error()}
	}
	reservation.SupervisorExecutionID = cronPauseExecutionID(reservation)
	result, err := p.CronPauseEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(err, effect.ErrResourceBlocked) {
		if blocking, found, lookupErr := p.CronPauseEffects.store.UnresolvedForResource(ctx, authority, reservation.Resource); lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking cron pause lookup failed", Cause: lookupErr})
		} else if found {
			if _, recoverErr := p.recoverCronBlockingEffect(ctx, blocking); recoverErr != nil {
				return deferredResult(claim, recoverErr)
			}
			// The recovered result belongs to the blocker, never to this claim.
			// Retry only after Reserve observes that the resource gate is clear.
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking cron pause reconciled; retry reservation"})
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron pause effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron pause request failed"}
	}
	return p.completeCronPause(ctx, claim, r, result)
}

func (p *Pipeline) completeCronPause(ctx context.Context, claim store.OperationClaim, r cronPauseRequest, result effect.ExecuteResult) *OperationResult {
	message := fmt.Sprintf("%s cron process %q paused", r.App, r.Process)
	metadata := map[string]interface{}{"process": r.Process, "schedule": r.Schedule, "timezone": r.TimeZone, "jobId": r.JobID, "version": r.Version, "modifyIndex": r.ModifyIndex, "effectId": result.EffectID, "effectReused": result.Reused}
	if err := p.finishCronPauseIntent(ctx, claim, r, message, metadata); err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: result.EffectID, Resource: cronPauseResource(r), Reason: "persist cron pause state", Cause: err})
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, finished: true}
}
func (p *Pipeline) finishCronPauseIntent(ctx context.Context, claim store.OperationClaim, r cronPauseRequest, message string, metadata map[string]interface{}) error {
	if p.FinishCronPauseIntent != nil {
		return p.FinishCronPauseIntent(ctx, claim, r.App, r.Process, r.Schedule, message, metadata)
	}
	if p.DB == nil {
		return fmt.Errorf("cron state store is unavailable")
	}
	return p.DB.FinishCronPauseClaimedOperation(ctx, claim, r.App, r.Process, r.Schedule, message, metadata)
}

type nomadCronPauseSupervisor struct {
	client *nomad.Client
	db     *store.DB
}

func (s *nomadCronPauseSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, err := cronPauseRequestFromReservation(r)
	return err
}
func (s *nomadCronPauseSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	q, err := cronPauseRequestFromReservation(r)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	claim, err := store.NewOperationClaim(r.OperationClaim.OperationID, r.OperationClaim.OwnerID, r.OperationClaim.Generation)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if err = s.db.CheckOperationClaim(ctx, claim); err != nil {
		return effect.ExecutionIdentity{}, err
	}
	state, err := s.client.PeriodicJobSchedule(q.JobID)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	if state.Paused || !cronPauseIntentMatches(q, state, true) {
		return effect.ExecutionIdentity{}, fmt.Errorf("Nomad periodic job no longer matches cron pause intent")
	}
	err = s.client.PausePeriodicJob(q.JobID, q.ModifyIndex, r.SupervisorExecutionID)
	if errors.Is(err, nomad.ErrJobRevisionChanged) {
		// Nomad acknowledged that the guarded write did not apply. Do not
		// reinterpret a later independently-paused parent as this effect.
		return effect.ExecutionIdentity{}, err
	}
	if err != nil { // A transport failure may follow a committed CAS update; Query is authoritative.
		if state, checkErr := s.client.PeriodicJobSchedule(q.JobID); checkErr != nil || !state.Paused || state.CronPauseEffectID != r.SupervisorExecutionID || !cronPauseIntentMatches(q, state, false) {
			return effect.ExecutionIdentity{}, err
		}
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-cron-pause:" + r.SupervisorExecutionID}, nil
}
func (s *nomadCronPauseSupervisor) Query(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	q, err := cronPauseRequestFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	state, err := s.client.PeriodicJobSchedule(q.JobID)
	if err != nil {
		return effect.Observation{}, err
	}
	if !cronPauseIntentMatches(q, state, false) {
		return effect.Observation{}, fmt.Errorf("Nomad periodic job no longer matches cron pause intent")
	}
	// Omit Nomad's mutable status and ModifyIndex from the result: recovery
	// retrieves this output later and must validate the same stopped evidence
	// even after Nomad advances bookkeeping indexes.
	out, _ := json.Marshal(map[string]interface{}{"jobId": q.JobID, "paused": state.Paused, "schedule": state.Schedule, "timezone": state.TimeZone, "cronPauseEffectId": state.CronPauseEffectID})
	id.Supervisor, id.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if id.RuntimeInstanceID == "" {
		id.RuntimeInstanceID = "nomad-cron-pause:" + r.SupervisorExecutionID
	}
	phase := effect.SupervisorUnknown
	if state.Paused && state.CronPauseEffectID == r.SupervisorExecutionID {
		phase = effect.SupervisorSucceeded
	}
	return effect.Observation{Identity: id, Phase: phase, Output: out, Evidence: effect.RawEvidence{Source: "nomad.job-status", Reference: q.JobID, Payload: out}}, nil
}
func (s *nomadCronPauseSupervisor) Revoke(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, id)
}
func (s *nomadCronPauseSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity, _ string) ([]byte, error) {
	o, e := s.Query(ctx, r, id)
	return o.Output, e
}
func cronPauseRequestFromReservation(r effect.Reservation) (cronPauseRequest, error) {
	var q cronPauseRequest
	if err := json.Unmarshal(r.LaunchPayload, &q); err != nil {
		return q, err
	}
	return cronPauseRequestFromOperation(&model.Operation{App: q.App, Payload: map[string]interface{}{"process": q.Process, "schedule": q.Schedule, "timezone": q.TimeZone, "jobId": q.JobID, "version": q.Version, "modifyIndex": q.ModifyIndex}})
}

func cronPauseIntentMatches(q cronPauseRequest, state *nomad.PeriodicJobInfo, requireRevision bool) bool {
	if state == nil || state.JobID != q.JobID || state.Schedule != q.Schedule || state.TimeZone != q.TimeZone {
		return false
	}
	return !requireRevision || (state.Version == q.Version && state.ModifyIndex == q.ModifyIndex)
}

func uint64FromMap(values map[string]interface{}, key string) uint64 {
	if values == nil {
		return 0
	}
	switch v := values[key].(type) {
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		if err == nil && n > 0 {
			return n
		}
	case uint64:
		return v
	case uint:
		return uint64(v)
	case int:
		if v > 0 {
			return uint64(v)
		}
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case float64:
		if v > 0 && v == float64(uint64(v)) {
			return uint64(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return uint64(n)
		}
	}
	return 0
}

type nomadCronPauseVerifier struct{}

func (nomadCronPauseVerifier) Verify(_ context.Context, r effect.Record, o effect.Observation) (effect.Verification, error) {
	if o.Phase != effect.SupervisorSucceeded || o.Identity.RuntimeInstanceID == "" || o.Identity.SupervisorExecutionID != r.Reservation.SupervisorExecutionID {
		return effect.Verification{}, fmt.Errorf("Nomad cron pause has no verified stopped outcome")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: r.Reservation.InputDigest, ResultDigest: effect.DigestInput(o.Output), ResultReference: o.Evidence.Reference, SupervisorExecutionID: o.Identity.SupervisorExecutionID, RuntimeInstanceID: o.Identity.RuntimeInstanceID, EvidenceSource: o.Evidence.Source, EvidenceReference: o.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
