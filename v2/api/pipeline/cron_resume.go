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

const nomadCronResumeStage = "app.cron-resume.nomad"

type NomadCronResumeEffects struct {
	executor *effect.Executor
	store    *store.PGEffectStore
	client   *nomad.Client
}

func NewNomadCronResumeEffects(db *store.DB, client *nomad.Client, p *Pipeline) (*NomadCronResumeEffects, error) {
	if client == nil || p == nil {
		return nil, fmt.Errorf("cron resume runtime is unavailable")
	}
	es, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	e := &NomadCronResumeEffects{store: es, client: client}
	e.executor = &effect.Executor{Store: es, Supervisor: &nomadCronResumeSupervisor{client: client, db: db, pipeline: p}, Verifier: nomadCronResumeVerifier{}}
	return e, nil
}
func (e *NomadCronResumeEffects) available() bool {
	return e != nil && e.executor != nil && e.store != nil && e.client != nil
}
func (p *Pipeline) CronResumeAvailable() bool {
	return p != nil && p.DB != nil && p.CronResumeEffects.available()
}

type cronResumeRequest struct {
	App              string `json:"app"`
	Process          string `json:"process"`
	Schedule         string `json:"schedule"`
	TimeZone         string `json:"timezone"`
	JobID            string `json:"jobId"`
	ImageTag         string `json:"imageTag"`
	SpecDigest       string `json:"specDigest"`
	DeliveryRevision uint64 `json:"deliveryRevision,omitempty"`
	Version          uint64 `json:"version"`
	ModifyIndex      uint64 `json:"modifyIndex"`
}

func cronResumeRequestFromOperation(op *model.Operation) (cronResumeRequest, error) {
	if op == nil {
		return cronResumeRequest{}, fmt.Errorf("cron resume operation is required")
	}
	r := cronResumeRequest{App: op.App, Process: stringFromMap(op.Payload, "process"), Schedule: stringFromMap(op.Payload, "schedule"), TimeZone: stringFromMap(op.Payload, "timezone"), JobID: stringFromMap(op.Payload, "jobId"), ImageTag: stringFromMap(op.Payload, "imageTag"), SpecDigest: stringFromMap(op.Payload, "specDigest"), DeliveryRevision: uint64FromMap(op.Payload, "deliveryRevision"), Version: uint64FromMap(op.Payload, "version"), ModifyIndex: uint64FromMap(op.Payload, "modifyIndex")}
	if strings.TrimSpace(r.App) == "" || strings.TrimSpace(r.Process) == "" || strings.TrimSpace(r.Schedule) == "" || strings.TrimSpace(r.ImageTag) == "" || len(r.SpecDigest) != 71 || !strings.HasPrefix(r.SpecDigest, "sha256:") || r.JobID != r.App+"-"+r.Process || r.Version == 0 || r.ModifyIndex == 0 {
		return cronResumeRequest{}, fmt.Errorf("cron resume descriptor is invalid")
	}
	return r, nil
}
func cronResumeResource(r cronResumeRequest) string { return "app/" + r.App + "/cron/" + r.Process }
func cronResumeExecutionID(r effect.Reservation) string {
	sum := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "nomad-cron-resume-" + hex.EncodeToString(sum[:16])
}
func cronResumeRequestFromReservation(res effect.Reservation) (cronResumeRequest, error) {
	var q cronResumeRequest
	if err := json.Unmarshal(res.LaunchPayload, &q); err != nil {
		return q, err
	}
	return cronResumeRequestFromOperation(&model.Operation{App: q.App, Payload: map[string]interface{}{"process": q.Process, "schedule": q.Schedule, "timezone": q.TimeZone, "jobId": q.JobID, "imageTag": q.ImageTag, "specDigest": q.SpecDigest, "deliveryRevision": q.DeliveryRevision, "version": q.Version, "modifyIndex": q.ModifyIndex}})
}
func cronResumeIntentMatches(q cronResumeRequest, state *nomad.PeriodicJobInfo, requireRevision bool) bool {
	if state == nil || state.JobID != q.JobID || state.Schedule != q.Schedule || state.TimeZone != q.TimeZone {
		return false
	}
	return !requireRevision || (state.Version == q.Version && state.ModifyIndex == q.ModifyIndex)
}

// cronResumeJob reconstructs all private launch material inside the claimed
// worker. Neither secrets nor database URLs are stored in the operation or
// effect descriptor. The declared process and exact signed schedule must
// still match before any remote write.
func (p *Pipeline) cronResumeJob(ctx context.Context, q cronResumeRequest) (*nomadapi.Job, error) {
	spec, err := p.findSpec(q.App)
	if err != nil {
		return nil, err
	}
	specDigest, err := model.InfraSpecDigest(spec)
	if err != nil || specDigest != q.SpecDigest {
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
	schedule := proc.Schedule
	if state != nil && state.Schedule != "" {
		schedule = state.Schedule
	}
	if schedule != q.Schedule || state == nil || !state.Paused {
		return nil, fmt.Errorf("cron resume intent is stale")
	}
	proc.Schedule = q.Schedule
	env := make(map[string]string)
	if p.Secrets != nil {
		secretEnv, err := p.Secrets.EnvMap(q.App)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("resolve secrets: %w", err)
		}
		for k, v := range secretEnv {
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
		material, err := p.RunningDeliveryRevision(ctx, spec, regions[0].NomadRegion, q.JobID)
		if err != nil {
			return nil, err
		}
		if q.DeliveryRevision == 0 || uint64(material.Revision) != q.DeliveryRevision {
			return nil, fmt.Errorf("database delivery revision changed since cron resume acceptance")
		}
		revision = material.Revision
	} else if q.DeliveryRevision != 0 {
		return nil, fmt.Errorf("database delivery requirements changed since cron resume acceptance")
	}
	job := nomad.TranslatePeriodicForRegionAt(spec, q.Process, proc, q.ImageTag, env, regions[0], revision)
	if job == nil || job.ID == nil || *job.ID != q.JobID || job.Periodic == nil || job.Periodic.Spec == nil || *job.Periodic.Spec != q.Schedule {
		return nil, fmt.Errorf("translated cron job does not match signed intent")
	}
	return job, nil
}

func (p *Pipeline) executeCronResume(ctx context.Context, op *model.Operation, claim store.OperationClaim) *OperationResult {
	if !p.CronResumeAvailable() {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "durable app.cron-resume execution is unavailable"}
	}
	q, err := cronResumeRequestFromOperation(op)
	if err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: err.Error()}
	}
	if err := p.DB.CheckOperationClaim(ctx, claim); err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronResumeResource(q), Reason: "cron resume claim is no longer current", Cause: err})
	}
	authority, err := p.CronResumeEffects.store.Authority(ctx)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronResumeResource(q), Reason: "control authority is unavailable", Cause: err})
	}
	if prior, found, lookupErr := p.CronResumeEffects.store.LatestForOperation(ctx, op.ID, nomadCronResumeStage); lookupErr != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronResumeResource(q), Reason: "prior cron resume lookup failed", Cause: lookupErr})
	} else if found {
		stored, storedErr := cronResumeRequestFromReservation(prior.Reservation)
		if storedErr != nil || stored != q {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron resume effect descriptor does not match signed operation intent"}
		}
		result, recoverErr := p.CronResumeEffects.executor.Recover(ctx, prior)
		if recoverErr != nil {
			return deferredResult(claim, recoverErr)
		}
		if result.Outcome != effect.OutcomeSucceeded {
			return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron resume request failed"}
		}
		return p.completeCronResume(ctx, claim, q, result)
	}
	current, err := p.CronResumeEffects.client.PeriodicJobSchedule(q.JobID)
	if err != nil {
		return deferredResult(claim, &effect.PendingError{Resource: cronResumeResource(q), Reason: "read periodic job state", Cause: err})
	}
	if !current.Paused || !cronResumeIntentMatches(q, current, true) {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad periodic job no longer matches cron resume intent"}
	}
	// Build before reserve so deterministic invalid material cannot strand a
	// resource gate. Launch rebuilds immediately before the guarded CAS.
	if _, err := p.cronResumeJob(ctx, q); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron resume material unavailable: " + err.Error()}
	}
	payload, _ := json.Marshal(q)
	reservation := effect.Reservation{Authority: authority, Resource: cronResumeResource(q), OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: nomadCronResumeStage, Supervisor: "nomad-cron-resume", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "fingerprint cron resume request: " + err.Error()}
	}
	reservation.SupervisorExecutionID = cronResumeExecutionID(reservation)
	result, err := p.CronResumeEffects.executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, LaunchMaterial: effect.LaunchMaterial{Subject: op.ID}})
	if errors.Is(err, effect.ErrResourceBlocked) {
		if blocking, found, lookupErr := p.CronResumeEffects.store.UnresolvedForResource(ctx, authority, reservation.Resource); lookupErr != nil {
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking cron resume lookup failed", Cause: lookupErr})
		} else if found {
			if _, recoverErr := p.recoverCronBlockingEffect(ctx, blocking); recoverErr != nil {
				return deferredResult(claim, recoverErr)
			}
			return deferredResult(claim, &effect.PendingError{Resource: reservation.Resource, Reason: "blocking cron effect reconciled; retry reservation"})
		}
	}
	if err != nil {
		if effect.IsDeferred(err) || errors.Is(err, effect.ErrResourceBlocked) {
			return deferredResult(claim, err)
		}
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "cron resume effect: " + err.Error()}
	}
	if result.Outcome != effect.OutcomeSucceeded {
		return &OperationResult{Claim: claim, Status: model.OperationFailed, Message: "Nomad cron resume request failed"}
	}
	return p.completeCronResume(ctx, claim, q, result)
}

func (p *Pipeline) completeCronResume(ctx context.Context, claim store.OperationClaim, q cronResumeRequest, result effect.ExecuteResult) *OperationResult {
	message := fmt.Sprintf("%s cron process %q resumed", q.App, q.Process)
	metadata := map[string]interface{}{"process": q.Process, "schedule": q.Schedule, "timezone": q.TimeZone, "jobId": q.JobID, "imageTag": q.ImageTag, "deliveryRevision": q.DeliveryRevision, "version": q.Version, "modifyIndex": q.ModifyIndex, "effectId": result.EffectID, "effectReused": result.Reused}
	var err error
	if p.FinishCronResumeIntent != nil {
		err = p.FinishCronResumeIntent(ctx, claim, q.App, q.Process, q.Schedule, message, metadata)
	} else {
		err = p.DB.FinishCronResumeClaimedOperation(ctx, claim, q.App, q.Process, q.Schedule, message, metadata)
	}
	if err != nil {
		return deferredResult(claim, &effect.PendingError{EffectID: result.EffectID, Resource: cronResumeResource(q), Reason: "persist cron resume state", Cause: err})
	}
	return &OperationResult{Claim: claim, Status: model.OperationSucceeded, Message: message, Metadata: metadata, finished: true}
}

type nomadCronResumeSupervisor struct {
	client   *nomad.Client
	db       *store.DB
	pipeline *Pipeline
}

func (s *nomadCronResumeSupervisor) Prepare(_ context.Context, r effect.Reservation) error {
	_, err := cronResumeRequestFromReservation(r)
	return err
}
func (s *nomadCronResumeSupervisor) Launch(ctx context.Context, r effect.Reservation, _ effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	q, err := cronResumeRequestFromReservation(r)
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
	if !state.Paused || !cronResumeIntentMatches(q, state, true) {
		return effect.ExecutionIdentity{}, fmt.Errorf("Nomad periodic job no longer matches cron resume intent")
	}
	job, err := s.pipeline.cronResumeJob(ctx, q)
	if err != nil {
		return effect.ExecutionIdentity{}, err
	}
	err = s.client.ResumePeriodicJobWithReplacement(q.JobID, q.ModifyIndex, r.SupervisorExecutionID, job)
	if errors.Is(err, nomad.ErrJobRevisionChanged) {
		return effect.ExecutionIdentity{}, err
	}
	if err != nil {
		if after, checkErr := s.client.PeriodicJobSchedule(q.JobID); checkErr != nil || after.Paused || after.CronResumeEffectID != r.SupervisorExecutionID || !cronResumeIntentMatches(q, after, false) {
			return effect.ExecutionIdentity{}, err
		}
	}
	return effect.ExecutionIdentity{Supervisor: r.Supervisor, SupervisorExecutionID: r.SupervisorExecutionID, RuntimeInstanceID: "nomad-cron-resume:" + r.SupervisorExecutionID}, nil
}
func (s *nomadCronResumeSupervisor) Query(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	q, err := cronResumeRequestFromReservation(r)
	if err != nil {
		return effect.Observation{}, err
	}
	state, err := s.client.PeriodicJobSchedule(q.JobID)
	if err != nil {
		return effect.Observation{}, err
	}
	if !cronResumeIntentMatches(q, state, false) {
		return effect.Observation{}, fmt.Errorf("Nomad periodic job no longer matches cron resume intent")
	}
	out, _ := json.Marshal(map[string]interface{}{"jobId": q.JobID, "paused": state.Paused, "schedule": state.Schedule, "timezone": state.TimeZone, "cronResumeEffectId": state.CronResumeEffectID})
	id.Supervisor, id.SupervisorExecutionID = r.Supervisor, r.SupervisorExecutionID
	if id.RuntimeInstanceID == "" {
		id.RuntimeInstanceID = "nomad-cron-resume:" + r.SupervisorExecutionID
	}
	phase := effect.SupervisorUnknown
	if !state.Paused && state.CronResumeEffectID == r.SupervisorExecutionID {
		phase = effect.SupervisorSucceeded
	}
	return effect.Observation{Identity: id, Phase: phase, Output: out, Evidence: effect.RawEvidence{Source: "nomad.job-status", Reference: q.JobID, Payload: out}}, nil
}
func (s *nomadCronResumeSupervisor) Revoke(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.Query(ctx, r, id)
}
func (s *nomadCronResumeSupervisor) RetrieveResult(ctx context.Context, r effect.Reservation, id effect.ExecutionIdentity, _ string) ([]byte, error) {
	o, e := s.Query(ctx, r, id)
	return o.Output, e
}

type nomadCronResumeVerifier struct{}

func (p *Pipeline) recoverCronBlockingEffect(ctx context.Context, record effect.Record) (effect.ExecuteResult, error) {
	switch record.Reservation.Stage {
	case nomadCronPauseStage:
		if !p.CronPauseAvailable() {
			return effect.ExecuteResult{}, fmt.Errorf("cron pause reconciler is unavailable")
		}
		return p.CronPauseEffects.executor.Recover(ctx, record)
	case nomadCronResumeStage:
		if !p.CronResumeAvailable() {
			return effect.ExecuteResult{}, fmt.Errorf("cron resume reconciler is unavailable")
		}
		return p.CronResumeEffects.executor.Recover(ctx, record)
	default:
		return effect.ExecuteResult{}, fmt.Errorf("unrecognized cron effect stage %q", record.Reservation.Stage)
	}
}

func (nomadCronResumeVerifier) Verify(_ context.Context, r effect.Record, o effect.Observation) (effect.Verification, error) {
	if o.Phase != effect.SupervisorSucceeded || o.Identity.RuntimeInstanceID == "" || o.Identity.SupervisorExecutionID != r.Reservation.SupervisorExecutionID {
		return effect.Verification{}, fmt.Errorf("Nomad cron resume has no verified running outcome")
	}
	return effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: r.Reservation.InputDigest, ResultDigest: effect.DigestInput(o.Output), ResultReference: o.Evidence.Reference, SupervisorExecutionID: o.Identity.SupervisorExecutionID, RuntimeInstanceID: o.Identity.RuntimeInstanceID, EvidenceSource: o.Evidence.Source, EvidenceReference: o.Evidence.Reference, ObservedAt: time.Now().UTC()}, nil
}
