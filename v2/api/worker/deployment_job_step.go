package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/effect"
	"norn/v2/api/nomad"
)

type DeploymentJobEffectStore interface {
	Reserve(context.Context, effect.Reservation) (effect.ReservationResult, error)
	MarkSubmitAttempt(context.Context, effect.Token) (bool, error)
	SubmitAttempted(context.Context, effect.Token) (bool, error)
	MarkLaunched(context.Context, effect.Token, effect.ExecutionIdentity) error
	Complete(context.Context, effect.Token, effect.Completion) error
}

type DeploymentJobRemote interface {
	CheckManagedJobInputs(context.Context, string, nomad.ManagedJobInputRequirements, map[string]string) error
	RegisterDeploymentJobCAS(context.Context, nomad.CASDeploymentJobRequest) (string, error)
	LookupDeploymentJobRevision(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobObservation, error)
	ObserveDeploymentJobHealth(context.Context, nomad.CASDeploymentJobRequest) (nomad.DeploymentJobHealthObservation, error)
}

var _ DeploymentJobRemote = (*nomad.Client)(nil)

type DeploymentJobEffectState string

const (
	DeploymentJobEffectObserved   DeploymentJobEffectState = "observed"
	DeploymentJobEffectUnresolved DeploymentJobEffectState = "unresolved"
)

type DeploymentJobEffectDecision struct {
	State       DeploymentJobEffectState
	EffectID    string
	Attempted   bool
	Observation nomad.DeploymentJobObservation
}

// EnsureDeploymentJobEffect admits one private service-job submission under
// the etcd effect gate. Only the caller that creates the durable attempt
// marker may send a Nomad CAS request. A failed or lost response goes through
// the same readback as recovery; 404 never licenses another submission.
func EnsureDeploymentJobEffect(ctx context.Context, effects DeploymentJobEffectStore, remote DeploymentJobRemote, reservation effect.Reservation, job *nomadapi.Job) (DeploymentJobEffectDecision, error) {
	if effects == nil || remote == nil || job == nil {
		return DeploymentJobEffectDecision{}, fmt.Errorf("deployment job execution is unavailable")
	}
	request, input, err := deploymentJobRequestFromReservation(reservation)
	if err != nil || !exactDeploymentServiceJob(job, input, request) {
		return DeploymentJobEffectDecision{}, fmt.Errorf("deployment job differs from reserved effect")
	}
	request.Job = job
	if input.JobID != "" {
		if err := remote.CheckManagedJobInputs(ctx, input.NomadRegion, *input.ManagedInputs, input.ExpectedDatabaseTargets); err != nil {
			return DeploymentJobEffectDecision{}, err
		}
	}
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil {
		return DeploymentJobEffectDecision{}, err
	}
	if reserved.Record.Token.EffectID == "" || reserved.Record.Token.Generation <= 0 ||
		reserved.Record.Reservation.Authority != reservation.Authority || reserved.Record.Reservation.Resource != reservation.Resource ||
		reserved.Record.Reservation.InputDigest != reservation.InputDigest || reserved.Record.Reservation.SupervisorExecutionID != reservation.SupervisorExecutionID ||
		reserved.Record.Reservation.OperationClaim.OperationID != reservation.OperationClaim.OperationID {
		return DeploymentJobEffectDecision{}, fmt.Errorf("deployment effect store returned different work")
	}
	if reserved.Created {
		allowed, err := effects.MarkSubmitAttempt(ctx, reserved.Record.Token)
		if err != nil {
			return DeploymentJobEffectDecision{}, err
		}
		if allowed {
			// The response is not proof. Even an ambiguous or conflict response
			// must be reconciled from the versioned Nomad job readback.
			_, _ = remote.RegisterDeploymentJobCAS(ctx, request)
		}
	}
	return ObserveDeploymentJobEffect(ctx, effects, remote, reserved.Record)
}

// ObserveDeploymentJobEffect is safe for a successor claim: it never writes
// to Nomad. A missing attempt marker or Nomad 404 remains unresolved until a
// separate fenced recovery or revocation protocol is implemented.
func ObserveDeploymentJobEffect(ctx context.Context, effects DeploymentJobEffectStore, remote DeploymentJobRemote, record effect.Record) (DeploymentJobEffectDecision, error) {
	if effects == nil || remote == nil || record.Token.EffectID == "" ||
		(record.Lifecycle != effect.LifecycleReserved && record.Lifecycle != effect.LifecycleLaunched) {
		return DeploymentJobEffectDecision{}, fmt.Errorf("deployment effect recovery is unavailable")
	}
	request, _, err := deploymentJobRequestFromReservation(record.Reservation)
	if err != nil {
		return DeploymentJobEffectDecision{}, err
	}
	attempted, err := effects.SubmitAttempted(ctx, record.Token)
	if err != nil {
		return DeploymentJobEffectDecision{}, err
	}
	decision := DeploymentJobEffectDecision{State: DeploymentJobEffectUnresolved, EffectID: record.Token.EffectID, Attempted: attempted}
	if !attempted {
		return decision, nil
	}
	observed, err := remote.LookupDeploymentJobRevision(ctx, request)
	if err != nil || observed.State != nomad.DeploymentJobFound || observed.JobModifyIndex <= request.ExpectedJobModifyIndex {
		return decision, nil
	}
	decision.Observation = observed
	if record.Lifecycle == effect.LifecycleReserved {
		identity := effect.ExecutionIdentity{Supervisor: record.Reservation.Supervisor, SupervisorExecutionID: record.Reservation.SupervisorExecutionID,
			RuntimeInstanceID: fmt.Sprintf("nomad-job:%s:%s:%d", request.Region, request.EffectiveJobID(), observed.JobModifyIndex)}
		if err := effects.MarkLaunched(ctx, record.Token, identity); err != nil {
			return decision, nil
		}
	}
	decision.State = DeploymentJobEffectObserved
	return decision, nil
}

// CompleteDeploymentJobEffect releases the effect gate only after the original
// launched job revision has healthy allocations. Pending or uncertain health
// leaves the durable effect for a later observation under a successor claim.
func CompleteDeploymentJobEffect(ctx context.Context, effects DeploymentJobEffectStore, remote DeploymentJobRemote, record effect.Record) (DeploymentJobEffectDecision, error) {
	if effects == nil || remote == nil || record.Token.EffectID == "" || record.Token.Generation <= 0 ||
		record.Lifecycle != effect.LifecycleLaunched || record.Execution.Supervisor != record.Reservation.Supervisor ||
		record.Execution.SupervisorExecutionID != record.Reservation.SupervisorExecutionID {
		return DeploymentJobEffectDecision{}, fmt.Errorf("deployment job completion is unavailable")
	}
	request, _, err := deploymentJobRequestFromReservation(record.Reservation)
	if err != nil {
		return DeploymentJobEffectDecision{}, err
	}
	attempted, err := effects.SubmitAttempted(ctx, record.Token)
	if err != nil {
		return DeploymentJobEffectDecision{}, err
	}
	decision := DeploymentJobEffectDecision{State: DeploymentJobEffectUnresolved, EffectID: record.Token.EffectID, Attempted: attempted}
	if !attempted {
		return decision, nil
	}
	health, err := remote.ObserveDeploymentJobHealth(ctx, request)
	if err != nil || health.State != nomad.DeploymentJobHealthReady ||
		health.JobModifyIndex <= request.ExpectedJobModifyIndex || len(health.AllocationIDs) == 0 ||
		record.Execution.RuntimeInstanceID != fmt.Sprintf("nomad-job:%s:%s:%d", request.Region, request.EffectiveJobID(), health.JobModifyIndex) {
		return decision, nil
	}
	result, err := json.Marshal(struct {
		App            string   `json:"app"`
		JobID          string   `json:"jobId"`
		DeploymentID   string   `json:"deploymentId"`
		OperationID    string   `json:"operationId"`
		ExecutionID    string   `json:"executionId"`
		JobDigest      string   `json:"jobDigest"`
		JobVersion     uint64   `json:"jobVersion"`
		JobModifyIndex uint64   `json:"jobModifyIndex"`
		AllocationIDs  []string `json:"allocationIds"`
	}{request.App, request.EffectiveJobID(), request.DeploymentID, request.OperationID, request.ExecutionID, request.JobDigest,
		health.JobVersion, health.JobModifyIndex, health.AllocationIDs})
	if err != nil {
		return decision, err
	}
	completion := effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: effect.Verification{
		Decision: effect.VerificationSucceeded, InputDigest: record.Reservation.InputDigest,
		ResultDigest: effect.DigestInput(result), ResultReference: record.Execution.RuntimeInstanceID,
		SupervisorExecutionID: record.Reservation.SupervisorExecutionID, RuntimeInstanceID: record.Execution.RuntimeInstanceID,
		EvidenceSource: "nomad-job-health", EvidenceReference: record.Execution.RuntimeInstanceID,
		ObservedAt: time.Now().UTC(),
	}}
	if err := effects.Complete(ctx, record.Token, completion); err != nil {
		return decision, err
	}
	decision.State = DeploymentJobEffectObserved
	decision.Observation = nomad.DeploymentJobObservation{State: nomad.DeploymentJobFound, Version: health.JobVersion, JobModifyIndex: health.JobModifyIndex}
	return decision, nil
}

func deploymentJobRequestFromReservation(r effect.Reservation) (nomad.CASDeploymentJobRequest, nomad.DeploymentJobEffectInput, error) {
	var input nomad.DeploymentJobEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		return nomad.CASDeploymentJobRequest{}, input, fmt.Errorf("deployment effect descriptor is invalid")
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || digest != r.InputDigest || r.Stage != "app.deploy.nomad.submit" || r.Supervisor != "nomad-deployment" ||
		r.OperationClaim.OperationID == "" || r.SupervisorExecutionID == "" || input.App == "" || input.DeploymentID == "" ||
		input.Region == "" || input.NomadRegion == "" || input.ImageTag == "" || input.SpecDigest == "" || input.JobDigest == "" ||
		r.Resource != "app/"+input.App+"/deploy/"+input.Region {
		return nomad.CASDeploymentJobRequest{}, input, fmt.Errorf("deployment effect descriptor is incomplete")
	}
	if input.JobID != "" {
		want, err := nomad.ManagedDeploymentJobID(input.App, input.Region, input.DeploymentID)
		if err != nil || input.JobID != want || input.ExpectedJobModifyIndex != 0 || input.ManagedInputs == nil ||
			input.ManagedInputs.JobID != want || input.ManagedInputs.VariablePath != nomad.DatabaseVariablePath(want) {
			return nomad.CASDeploymentJobRequest{}, input, fmt.Errorf("managed deployment job identity is invalid")
		}
	} else if input.ManagedInputs != nil || len(input.ExpectedDatabaseTargets) > 0 {
		return nomad.CASDeploymentJobRequest{}, input, fmt.Errorf("legacy deployment cannot declare managed inputs")
	}
	request := nomad.CASDeploymentJobRequest{App: input.App, JobID: input.JobID, Region: input.NomadRegion, ExpectedJobModifyIndex: input.ExpectedJobModifyIndex,
		DeploymentID: input.DeploymentID, SpecDigest: input.SpecDigest, OperationID: r.OperationClaim.OperationID,
		ExecutionID: r.SupervisorExecutionID, JobDigest: input.JobDigest, ImageTag: input.ImageTag}
	if input.JobID != "" {
		request.PlacementRegion = input.Region
	}
	return request, input, nil
}

func exactDeploymentServiceJob(job *nomadapi.Job, input nomad.DeploymentJobEffectInput, request nomad.CASDeploymentJobRequest) bool {
	if job == nil || job.ID == nil || *job.ID != request.EffectiveJobID() || job.Region == nil || *job.Region != input.NomadRegion ||
		job.Type == nil || *job.Type != "service" || len(job.TaskGroups) == 0 ||
		job.Meta[nomad.DeploymentIDMeta] != request.DeploymentID || job.Meta[nomad.SpecDigestMeta] != request.SpecDigest ||
		job.Meta[nomad.DeploymentOperationIDMeta] != request.OperationID || job.Meta[nomad.DeploymentExecutionIDMeta] != request.ExecutionID ||
		job.Meta[nomad.DeploymentJobDigestMeta] != request.JobDigest {
		return false
	}
	for _, group := range job.TaskGroups {
		if group == nil || len(group.Tasks) == 0 {
			return false
		}
		for _, task := range group.Tasks {
			if task == nil || task.Driver != "docker" || task.Config["image"] != input.ImageTag {
				return false
			}
		}
	}
	digest, err := nomad.DigestDeploymentJob(job)
	return err == nil && digest == request.JobDigest
}
