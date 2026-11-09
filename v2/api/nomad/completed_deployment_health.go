package nomad

import (
	"context"
	"encoding/json"
	"fmt"
	"norn/v2/api/effect"
)

// VerifyCompletedDeploymentJobHealth reads the exact previously completed job
// without staging inputs or writing to Nomad. The original execution identity
// pins the job modify index so a replacement revision cannot inherit its proof.
func VerifyCompletedDeploymentJobHealth(ctx context.Context, remote *Client, record effect.Record) error {
	if remote == nil || record.Token.EffectID == "" || record.Token.Generation <= 0 || record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil ||
		record.Completion.Outcome != effect.OutcomeSucceeded || record.Completion.Verification.Decision != effect.VerificationSucceeded ||
		record.Completion.Verification.InputDigest != record.Reservation.InputDigest || record.Completion.Verification.EvidenceSource != "nomad-job-health" ||
		record.Execution.Supervisor != record.Reservation.Supervisor || record.Execution.SupervisorExecutionID != record.Reservation.SupervisorExecutionID ||
		record.Completion.Verification.RuntimeInstanceID != record.Execution.RuntimeInstanceID {
		return fmt.Errorf("completed deployment health evidence is invalid")
	}
	request, _, err := DeploymentJobRequestFromReservation(record.Reservation)
	if err != nil {
		return err
	}
	health, err := remote.ObserveDeploymentJobHealth(ctx, request)
	if err != nil || health.State != DeploymentJobHealthReady || len(health.AllocationIDs) == 0 ||
		health.JobModifyIndex <= request.ExpectedJobModifyIndex ||
		record.Execution.RuntimeInstanceID != fmt.Sprintf("nomad-job:%s:%s:%d", request.Region, request.EffectiveJobID(), health.JobModifyIndex) {
		return fmt.Errorf("completed deployment job is not freshly healthy")
	}
	return nil
}

func DeploymentJobRequestFromReservation(r effect.Reservation) (CASDeploymentJobRequest, DeploymentJobEffectInput, error) {
	var input DeploymentJobEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		return CASDeploymentJobRequest{}, input, fmt.Errorf("deployment effect descriptor is invalid")
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || digest != r.InputDigest || r.Stage != "app.deploy.nomad.submit" || r.Supervisor != "nomad-deployment" ||
		r.OperationClaim.OperationID == "" || r.SupervisorExecutionID == "" || input.App == "" || input.DeploymentID == "" ||
		input.Region == "" || input.NomadRegion == "" || input.ImageTag == "" || input.SpecDigest == "" || input.JobDigest == "" ||
		r.Resource != "app/"+input.App+"/deploy/"+input.Region {
		return CASDeploymentJobRequest{}, input, fmt.Errorf("deployment effect descriptor is incomplete")
	}
	if input.JobID != "" {
		want, err := ManagedDeploymentJobID(input.App, input.Region, input.DeploymentID)
		if err != nil || input.JobID != want || input.ExpectedJobModifyIndex != 0 || input.ManagedInputs == nil ||
			input.ManagedInputs.JobID != want || input.ManagedInputs.Namespace != input.NomadNamespace || input.ManagedInputs.VariablePath != DatabaseVariablePath(want) {
			return CASDeploymentJobRequest{}, input, fmt.Errorf("managed deployment job identity is invalid")
		}
	} else if input.ManagedInputs != nil || len(input.ExpectedDatabaseTargets) > 0 {
		return CASDeploymentJobRequest{}, input, fmt.Errorf("legacy deployment cannot declare managed inputs")
	}
	request := CASDeploymentJobRequest{App: input.App, JobID: input.JobID, Region: input.NomadRegion, Namespace: input.NomadNamespace, ExpectedJobModifyIndex: input.ExpectedJobModifyIndex,
		DeploymentID: input.DeploymentID, SpecDigest: input.SpecDigest, OperationID: r.OperationClaim.OperationID,
		ExecutionID: r.SupervisorExecutionID, JobDigest: input.JobDigest, ImageTag: input.ImageTag}
	if input.JobID != "" {
		request.PlacementRegion = input.Region
	}
	return request, input, nil
}
