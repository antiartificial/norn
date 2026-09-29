package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/jackc/pgx/v5"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/store"
)

const nomadCronScheduleStage = "app.cron-schedule.nomad"

// NomadCronScheduleEffects is the sole write boundary for a periodic schedule
// replacement. The exact accepted source and previous Nomad revision are
// carried through the reservation; private job material is rebuilt by the
// claimed worker.
type NomadCronScheduleEffects struct {
	executor *effect.Executor
	store    *store.PGEffectStore
	client   *nomad.Client
}

func NewNomadCronScheduleEffects(db *store.DB, client *nomad.Client, p *Pipeline) (*NomadCronScheduleEffects, error) {
	if client == nil || p == nil {
		return nil, fmt.Errorf("cron schedule runtime is unavailable")
	}
	es, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	e := &NomadCronScheduleEffects{store: es, client: client}
	e.executor = &effect.Executor{Store: es, Supervisor: &nomadCronScheduleSupervisor{client: client, db: db, pipeline: p}, Verifier: nomadCronScheduleVerifier{}}
	return e, nil
}
func (e *NomadCronScheduleEffects) available() bool {
	return e != nil && e.executor != nil && e.store != nil && e.client != nil
}
func (p *Pipeline) CronScheduleAvailable() bool {
	return p != nil && p.DB != nil && p.CronScheduleEffects.available()
}

type cronScheduleRequest struct {
	App              string `json:"app"`
	Process          string `json:"process"`
	PreviousSchedule string `json:"previousSchedule"`
	Schedule         string `json:"schedule"`
	TimeZone         string `json:"timezone"`
	JobID            string `json:"jobId"`
	ImageTag         string `json:"imageTag"`
	SpecDigest       string `json:"specDigest"`
	DeliveryRevision uint64 `json:"deliveryRevision,omitempty"`
	Version          uint64 `json:"version"`
	ModifyIndex      uint64 `json:"modifyIndex"`
	Paused           bool   `json:"paused"`
}

func cronScheduleRequestFromOperation(op *model.Operation) (cronScheduleRequest, error) {
	if op == nil {
		return cronScheduleRequest{}, fmt.Errorf("cron schedule operation is required")
	}
	r := cronScheduleRequest{App: op.App, Process: stringFromMap(op.Payload, "process"), PreviousSchedule: stringFromMap(op.Payload, "previousSchedule"), Schedule: stringFromMap(op.Payload, "schedule"), TimeZone: stringFromMap(op.Payload, "timezone"), JobID: stringFromMap(op.Payload, "jobId"), ImageTag: stringFromMap(op.Payload, "imageTag"), SpecDigest: stringFromMap(op.Payload, "specDigest"), DeliveryRevision: uint64FromMap(op.Payload, "deliveryRevision"), Version: uint64FromMap(op.Payload, "version"), ModifyIndex: uint64FromMap(op.Payload, "modifyIndex")}
	if strings.TrimSpace(r.App) == "" || strings.TrimSpace(r.Process) == "" || strings.TrimSpace(r.PreviousSchedule) == "" || strings.TrimSpace(r.Schedule) == "" || strings.TrimSpace(r.ImageTag) == "" || len(r.SpecDigest) != 71 || !strings.HasPrefix(r.SpecDigest, "sha256:") || r.JobID != r.App+"-"+r.Process || r.Version == 0 || r.ModifyIndex == 0 {
		return cronScheduleRequest{}, fmt.Errorf("cron schedule descriptor is invalid")
	}
	if paused, ok := op.Payload["paused"].(bool); ok {
		r.Paused = paused
	} else {
		return cronScheduleRequest{}, fmt.Errorf("cron schedule descriptor is invalid")
	}
	return r, nil
}
func cronScheduleResource(r cronScheduleRequest) string { return "app/" + r.App + "/cron/" + r.Process }
func cronScheduleExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-cron-schedule-" + hex.EncodeToString(sum[:16])
}
func cronScheduleRequestFromReservation(res effect.Reservation) (cronScheduleRequest, error) {
	var q cronScheduleRequest
	if err := json.Unmarshal(res.LaunchPayload, &q); err != nil {
		return q, err
	}
	return cronScheduleRequestFromOperation(&model.Operation{App: q.App, Payload: map[string]interface{}{"process": q.Process, "previousSchedule": q.PreviousSchedule, "schedule": q.Schedule, "timezone": q.TimeZone, "jobId": q.JobID, "imageTag": q.ImageTag, "specDigest": q.SpecDigest, "deliveryRevision": q.DeliveryRevision, "version": q.Version, "modifyIndex": q.ModifyIndex, "paused": q.Paused}})
}
func cronScheduleIntentMatches(q cronScheduleRequest, s *nomad.PeriodicJobInfo, revision bool) bool {
	return s != nil && s.JobID == q.JobID && s.Schedule == q.PreviousSchedule && s.TimeZone == q.TimeZone && s.Paused == q.Paused && (!revision || (s.Version == q.Version && s.ModifyIndex == q.ModifyIndex))
}

func (p *Pipeline) cronScheduleJob(ctx context.Context, q cronScheduleRequest) (*nomadapi.Job, error) {
	spec, err := p.findSpec(q.App)
	if err != nil {
		return nil, err
	}
	digest, err := model.InfraSpecDigest(spec)
	if err != nil || digest != q.SpecDigest {
		return nil, fmt.Errorf("scheduled process spec changed since acceptance")
	}
	proc, ok := spec.Processes[q.Process]
	if !ok || proc.Schedule == "" || model.ResolveProcessTimezone(spec, proc) != q.TimeZone {
		return nil, fmt.Errorf("scheduled process changed since acceptance")
	}
	state, err := p.DB.GetCronState(ctx, q.App, q.Process)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read cron state: %w", err)
	}
	previous := proc.Schedule
	if state != nil && state.Schedule != "" {
		previous = state.Schedule
	}
	if previous != q.PreviousSchedule || (state != nil && state.Paused != q.Paused) {
		return nil, fmt.Errorf("cron schedule intent is stale")
	}
	proc.Schedule = q.Schedule
	env := map[string]string{}
	if p.Secrets != nil {
		values, e := p.Secrets.EnvMap(q.App)
		if e != nil && !os.IsNotExist(e) {
			return nil, fmt.Errorf("resolve private cron environment")
		}
		for k, v := range values {
			env[k] = v
		}
	}
	if conflicts := spec.DatabaseEnvConflicts(env); len(conflicts) > 0 {
		return nil, fmt.Errorf("app secrets conflict with delivered database bindings: %s", strings.Join(conflicts, ", "))
	}
	regions := spec.ResolvedRegions()
	if len(regions) == 0 {
		return nil, fmt.Errorf("app has no region")
	}
	revision := int64(0)
	if spec.NamedDatabases() && nomad.HasRuntimeDatabases(spec) {
		if p.DatabaseTargets == nil {
			return nil, fmt.Errorf("named databases require a database profile")
		}
		material, e := p.RunningDeliveryRevision(ctx, spec, regions[0].NomadRegion, q.JobID)
		if e != nil {
			return nil, e
		}
		if q.DeliveryRevision == 0 || uint64(material.Revision) != q.DeliveryRevision {
			return nil, fmt.Errorf("database delivery revision changed since schedule acceptance")
		}
		revision = material.Revision
	} else if q.DeliveryRevision != 0 {
		return nil, fmt.Errorf("database delivery requirements changed since schedule acceptance")
	}
	job := nomad.TranslatePeriodicForRegionAt(spec, q.Process, proc, q.ImageTag, env, regions[0], revision)
	if job == nil || job.ID == nil || *job.ID != q.JobID || job.Periodic == nil || job.Periodic.Spec == nil || *job.Periodic.Spec != q.Schedule {
		return nil, fmt.Errorf("translated cron job does not match signed intent")
	}
	return job, nil
}

func (p *Pipeline) executeCronSchedule(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CronScheduleAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.cron-schedule execution is unavailable"}
	}
	q, err := cronScheduleRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	if err = p.DB.CheckOperationClaim(ctx, claim); err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronScheduleResource(q), Reason: "cron schedule claim is no longer current", Cause: err})
	}
	authority, err := p.CronScheduleEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronScheduleResource(q), Reason: "control authority is unavailable", Cause: err})
	}
	if prior, found, e := p.CronScheduleEffects.store.LatestForOperation(ctx, op.ID, nomadCronScheduleStage); e != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronScheduleResource(q), Reason: "prior cron schedule lookup failed", Cause: e})
	} else if found {
		stored, se := cronScheduleRequestFromReservation(prior.Reservation)
		if se != nil || stored != q {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron schedule effect descriptor does not match signed operation intent"}
		}
		result, re := p.CronScheduleEffects.executor.Recover(ctx, prior)
		if re != nil {
			return deferredResult(claim, re)
		}
		if result.Outcome != effect.OutcomeSucceeded {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron schedule request failed"}
		}
		return p.completeCronSchedule(ctx, claim, q, result)
	}
	current, e := p.CronScheduleEffects.client.PeriodicJobSchedule(q.JobID)
	if e != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronScheduleResource(q), Reason: "read periodic job state", Cause: e})
	}
	if !cronScheduleIntentMatches(q, current, true) {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad periodic job no longer matches cron schedule intent"}
	}
	if _, e = p.cronScheduleJob(ctx, q); e != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron schedule launch material is unavailable or changed"}
	}
	payload, _ := json.Marshal(q)
	r := effect.Reservation{Authority: authority, Resource: cronScheduleResource(q), OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: nomadCronScheduleStage, Supervisor: "nomad-cron-schedule", LaunchPayload: payload}
	if r.InputDigest, err = effect.ComputeInputDigest(r); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint cron schedule request: " + err.Error()}
	}
	r.SupervisorExecutionID = cronScheduleExecutionID(r)
	result, e := p.CronScheduleEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: r, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(e, effect.ErrResourceBlocked) {
		if blocking, found, le := p.CronScheduleEffects.store.UnresolvedForResource(ctx, authority, r.Resource); le != nil {
			return deferredResult(claim, &effect.PendingError{Resource: r.Resource, Reason: "blocking cron schedule lookup failed", Cause: le})
		} else if found {
			if _, re := p.recoverCronBlockingEffect(ctx, blocking); re != nil {
				return deferredResult(claim, re)
			}
			return deferredResult(claim, &effect.PendingError{Resource: r.Resource, Reason: "blocking cron effect reconciled; retry reservation"})
		}
	}
	if e != nil {
		if effect.IsDeferred(e) || errors.Is(e, effect.ErrResourceBlocked) {
			return deferredResult(claim, e)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron schedule effect: " + e.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron schedule request failed"}
	}
	return p.completeCronSchedule(ctx, claim, q, result)
}
func (p *Pipeline) completeCronSchedule(ctx context.Context, claim store.OperationClaim, q cronScheduleRequest, result effect.ExecuteResult) *OperationResult {
	message := fmt.Sprintf("%s cron process %q schedule updated", q.App, q.Process)
	metadata := map[string]interface{}{"process": q.Process, "previousSchedule": q.PreviousSchedule, "schedule": q.Schedule, "timezone": q.TimeZone, "jobId": q.JobID, "imageTag": q.ImageTag, "deliveryRevision": q.DeliveryRevision, "version": q.Version, "modifyIndex": q.ModifyIndex, "paused": q.Paused, "effectId": result.EffectID, "effectReused": result.Reused}
	var err error
	if p.FinishCronScheduleIntent != nil {
		err = p.FinishCronScheduleIntent(ctx, claim, q.App, q.Process, q.Paused, q.Schedule, message, metadata)
	} else {
		err = p.DB.FinishCronScheduleClaimedOperation(ctx, claim, q.App, q.Process, q.Paused, q.Schedule, message, metadata)
	}
	if err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: result.EffectID, Resource: cronScheduleResource(q), Reason: "persist cron schedule state", Cause: err})
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, finished: true}
}

type nomadCronScheduleSupervisor struct {
	client   *nomad.Client
	db       *store.DB
	pipeline *Pipeline
}

func (s *nomadCronScheduleSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, e := cronScheduleRequestFromReservation(r)
	return e
}
func (s *nomadCronScheduleSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	q, e := cronScheduleRequestFromReservation(r)
	if e != nil {
		return effect.ExecutionIdentity{}, e
	}
	claim, e := store.NewOperationClaim(r.OperationClaim.OperationID, r.OperationClaim.OwnerID, r.OperationClaim.Generation)
	if e != nil {
		return effect.ExecutionIdentity{}, e
	}
	if e = s.db.CheckOperationClaim(ctx, claim); e != nil {
		return effect.ExecutionIdentity{}, e
	}
	state, e := s.client.PeriodicJobSchedule(q.JobID)
	if e != nil {
		return effect.ExecutionIdentity{}, e
	}
	if !cronScheduleIntentMatches(q, state, true) {
		return effect.ExecutionIdentity{}, fmt.Errorf("Nomad periodic job no longer matches cron schedule intent")
	}
	job, e := s.pipeline.cronScheduleJob(ctx, q)
	if e != nil {
		return effect.ExecutionIdentity{}, e
	}
	e = s.client.UpdatePeriodicJobSchedule(q.JobID, q.ModifyIndex, r.SupervisorExecutionID, job)
	if errors.Is(e, nomad.ErrJobRevisionChanged) {
		return effect.ExecutionIdentity{}, e
	}
	if e != nil {
		after, ce := s.client.PeriodicJobSchedule(q.JobID)
		if ce != nil || after.Schedule != q.Schedule || after.Paused != q.Paused || after.CronScheduleEffectID != r.SupervisorExecutionID {
			return effect.ExecutionIdentity{}, e
		}
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-cron-schedule:" + r.SupervisorExecutionID}, nil
}
func (s *nomadCronScheduleSupervisor) Query(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	q, e := cronScheduleRequestFromReservation(r)
	if e != nil {
		return effect.Observation{}, e
	}
	state, e := s.client.PeriodicJobSchedule(q.JobID)
	if e != nil {
		return effect.Observation{}, e
	}
	if state.JobID != q.JobID || state.Schedule != q.Schedule || state.TimeZone != q.TimeZone || state.Paused != q.Paused {
		return effect.Observation{}, fmt.Errorf("Nomad periodic job no longer matches cron schedule result")
	}
	out, _ := json.Marshal(map[string]interface{}{"jobId": q.JobID, "paused": state.Paused, "schedule": state.Schedule, "timezone": state.TimeZone, "cronScheduleEffectId": state.CronScheduleEffectID})
	id.Supervisor, id.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if id.RuntimeInstanceID == "" {
		id.RuntimeInstanceID = "nomad-cron-schedule:" + r.SupervisorExecutionID
	}
	phase := effect.SupervisorUnknown
	if state.CronScheduleEffectID == r.SupervisorExecutionID {
		phase = effect.SupervisorSucceeded
	}
	return effect.Observation{Identity: id, Phase: phase, Output: out, Evidence: effect.RawEvidence{Source: "nomad.job-status", Reference: q.JobID, Payload: out}}, nil
}
func (s *nomadCronScheduleSupervisor) Revoke(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, id)
}
func (s *nomadCronScheduleSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity, _ string) ([]byte, error) {
	o, e := s.Query(ctx, r, id)
	return o.Output, e
}

type nomadCronScheduleVerifier struct{}

func (nomadCronScheduleVerifier) Verify(_ context.Context, r effect.Record, o effect.Observation) (effect.Verification, error) {
	if o.Phase != effect.SupervisorSucceeded || o.Identity.RuntimeInstanceID == "" || o.Identity.SupervisorExecutionID != r.Reservation.SupervisorExecutionID {
		return effect.Verification{}, fmt.Errorf("Nomad cron schedule has no verified outcome")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: r.Reservation.InputDigest, ResultDigest: effect.DigestInput(o.Output), ResultReference: o.Evidence.Reference, SupervisorExecutionID: o.Identity.SupervisorExecutionID, RuntimeInstanceID: o.Identity.RuntimeInstanceID, EvidenceSource: o.Evidence.Source, EvidenceReference: o.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
