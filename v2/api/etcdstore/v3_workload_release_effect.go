package etcdstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
)

type workloadReleaseEffectDescriptor struct {
	Intent json.RawMessage `json:"intent"`
}

type workloadReleaseEffectIdentity struct {
	ID     string `json:"id"`
	App    string `json:"app"`
	Region string `json:"region"`
}

const (
	workloadReleaseStageTargetEffectStage = "app.workload-release.stage-target.nomad"
	workloadReleaseStageTargetSupervisor  = "nomad-workload-release-stage"
)

type workloadReleaseStageTargetEffectInput struct {
	PlanDigest   string `json:"planDigest"`
	TargetJobID  string `json:"targetJobId"`
	DeploymentID string `json:"deploymentId"`
	SpecDigest   string `json:"specDigest"`
	Image        string `json:"image"`
}

type workloadReleaseStageTargetAttempt struct {
	Token             effect.Token `json:"token"`
	InputDigest       string       `json:"inputDigest"`
	RuntimeInstanceID string       `json:"runtimeInstanceId"`
}

func workloadReleaseStageTargetExecutionID(operationID, planDigest string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + planDigest))
	return "wls-" + hex.EncodeToString(sum[:])
}

func (s *V3CanaryEffectReservations) validateWorkloadReleaseEffectReservation(ctx context.Context, r effect.Reservation, op model.Operation) (string, error) {
	if r.Stage != workloadReleaseStageTargetEffectStage {
		if effectPayloadString(op.Payload, "workloadReleaseIntent") == "" {
			index, err := s.operations.kv.Get(ctx, s.operations.operationAcceptanceIndexKey(op.ID))
			if err != nil || len(index.Kvs) != 1 {
				return "", fmt.Errorf("workload release effect has no signed plan acceptance")
			}
			acceptanceKey := string(index.Kvs[0].Value)
			loaded, err := s.operations.loadAcceptance(ctx, acceptanceKey)
			if err != nil || acceptanceKey != s.operations.acceptanceKey(loaded.record.Identity) ||
				loaded.record.Identity.Kind != op.Kind || loaded.record.Identity.Resource != "app/"+op.App ||
				loaded.record.Accepted.Operation.ID != op.ID {
				return "", fmt.Errorf("workload release effect acceptance link is invalid")
			}
			accepted, err := s.operations.replay(ctx, acceptanceKey, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
			acceptedBytes, acceptedErr := json.Marshal(accepted.Operation.Payload)
			currentBytes, currentErr := json.Marshal(op.Payload)
			if err != nil || acceptedErr != nil || currentErr != nil || !bytes.Equal(acceptedBytes, currentBytes) {
				return "", fmt.Errorf("workload release effect plan differs from signed acceptance")
			}
			planRaw := effectPayloadString(op.Payload, "workloadReleasePlan")
			var plan pipeline.WorkloadReleasePlan
			if planRaw == "" || json.Unmarshal([]byte(planRaw), &plan) != nil || plan.Validate() != nil {
				return "", fmt.Errorf("workload release effect has no accepted intent or plan")
			}
			targetResponse, err := s.operations.kv.Get(ctx, s.operations.workloadReleaseStagedTargetKey(op.ID))
			if err != nil || len(targetResponse.Kvs) != 1 {
				return "", fmt.Errorf("workload release effect has no immutable staged target binding")
			}
			record, err := decodeWorkloadReleaseStagedTarget(targetResponse.Kvs[0].Value, op.ID, plan)
			if err != nil {
				return "", err
			}
			intent, err := plan.Resolve(record.Binding, record.Binding.EffectID, record.Binding.ClaimGeneration)
			if err != nil {
				return "", err
			}
			intentBytes, err := json.Marshal(intent)
			if err != nil {
				return "", err
			}
			copyOperation := op
			copyOperation.Payload = make(map[string]any, len(op.Payload)+1)
			for key, value := range op.Payload {
				copyOperation.Payload[key] = value
			}
			copyOperation.Payload["workloadReleaseIntent"] = string(intentBytes)
			return validateWorkloadReleaseEffectReservation(r, copyOperation, s.operations.authority)
		}
		return validateWorkloadReleaseEffectReservation(r, op, s.operations.authority)
	}
	if r.Authority != s.operations.authority || r.OperationClaim.OperationID != op.ID || r.OperationClaim.OwnerID == "" || r.OperationClaim.Generation <= 0 ||
		r.Supervisor != workloadReleaseStageTargetSupervisor || r.SupervisorExecutionID == "" || op.Kind != "app.workload-release" {
		return "", fmt.Errorf("workload release staging effect does not match accepted operation and authority")
	}
	index, err := s.operations.kv.Get(ctx, s.operations.operationAcceptanceIndexKey(op.ID))
	if err != nil || len(index.Kvs) != 1 {
		return "", fmt.Errorf("workload release staging effect has no signed acceptance")
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.operations.loadAcceptance(ctx, key)
	if err != nil || key != s.operations.acceptanceKey(loaded.record.Identity) || loaded.record.Accepted.Operation.ID != op.ID {
		return "", fmt.Errorf("workload release staging acceptance link is invalid")
	}
	accepted, err := s.operations.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil || accepted.Operation.ID != op.ID || accepted.Operation.Kind != op.Kind || accepted.Operation.App != op.App {
		return "", fmt.Errorf("workload release staging acceptance failed verification: %v", err)
	}
	acceptedPayload, err := json.Marshal(accepted.Operation.Payload)
	currentPayload, currentErr := json.Marshal(op.Payload)
	if err != nil || currentErr != nil || !bytes.Equal(acceptedPayload, currentPayload) {
		return "", fmt.Errorf("workload release staging operation differs from signed acceptance")
	}
	rawPlan := effectPayloadString(accepted.Operation.Payload, "workloadReleasePlan")
	var plan pipeline.WorkloadReleasePlan
	if rawPlan == "" || json.Unmarshal([]byte(rawPlan), &plan) != nil || plan.Validate() != nil || plan.Intent.App != op.App {
		return "", fmt.Errorf("signed workload release plan is invalid for staging")
	}
	digest, err := plan.Digest()
	if err != nil {
		return "", err
	}
	var input workloadReleaseStageTargetEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		return "", fmt.Errorf("decode workload release staging effect: %w", err)
	}
	want := workloadReleaseStageTargetEffectInput{PlanDigest: digest, TargetJobID: plan.Intent.TargetJobID,
		DeploymentID: plan.Intent.TargetDeploymentID, SpecDigest: plan.Intent.TargetSpecDigest, Image: plan.Intent.TargetImage}
	if input != want || r.Resource != "app/"+plan.Intent.App+"/workload-release/"+plan.Intent.Region ||
		r.SupervisorExecutionID != workloadReleaseStageTargetExecutionID(op.ID, digest) {
		return "", fmt.Errorf("workload release staging effect differs from signed plan")
	}
	computed, err := effect.ComputeInputDigest(r)
	if err != nil || r.InputDigest != computed {
		return "", fmt.Errorf("workload release staging effect digest does not match reservation")
	}
	return plan.Intent.App, nil
}

func workloadReleaseStageTargetAttemptKey(prefix string, token effect.Token) string {
	return prefix + "/v3/effects/workload-release-attempts/" + token.EffectID
}

func validateWorkloadReleaseEffectReservation(r effect.Reservation, op model.Operation, authority string) (string, error) {
	if r.Authority != authority || r.OperationClaim.OperationID != op.ID || r.OperationClaim.OwnerID == "" || r.OperationClaim.Generation <= 0 ||
		r.Stage != "app.workload-release.stop-old.nomad" || r.Supervisor != "nomad-workload-release-stop" || r.SupervisorExecutionID == "" || op.Kind != "app.workload-release" {
		return "", fmt.Errorf("workload release effect reservation does not match accepted operation and authority")
	}
	var descriptor workloadReleaseEffectDescriptor
	if err := json.Unmarshal(r.LaunchPayload, &descriptor); err != nil || !json.Valid(descriptor.Intent) {
		return "", fmt.Errorf("decode workload release effect descriptor")
	}
	var intent, accepted workloadReleaseEffectIdentity
	if err := json.Unmarshal(descriptor.Intent, &intent); err != nil {
		return "", fmt.Errorf("decode workload release intent identity")
	}
	acceptedRaw := effectPayloadString(op.Payload, "workloadReleaseIntent")
	if acceptedRaw == "" || json.Unmarshal([]byte(acceptedRaw), &accepted) != nil || intent.ID == "" || intent.App == "" || intent.Region == "" ||
		accepted != intent || intent.App != op.App || r.Resource != "app/"+intent.App+"/workload-release/"+intent.Region ||
		strings.Contains(intent.App, "/") || strings.Contains(intent.Region, "/") {
		return "", fmt.Errorf("workload release effect intent differs from accepted operation")
	}
	var acceptedValue, intentValue interface{}
	if json.Unmarshal([]byte(acceptedRaw), &acceptedValue) != nil || json.Unmarshal(descriptor.Intent, &intentValue) != nil {
		return "", fmt.Errorf("workload release intent JSON is invalid")
	}
	acceptedCanonical, err := json.Marshal(acceptedValue)
	if err != nil {
		return "", err
	}
	intentCanonical, err := json.Marshal(intentValue)
	if err != nil || !bytes.Equal(acceptedCanonical, intentCanonical) {
		return "", fmt.Errorf("workload release effect intent bytes differ from accepted operation")
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || r.InputDigest != digest {
		return "", fmt.Errorf("workload release effect input digest does not match reservation")
	}
	return intent.App, nil
}
