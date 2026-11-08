package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"norn/v2/api/nomad"
)

const WorkloadReleasePlanSchema = "norn.workload-release-plan/v1"

// WorkloadReleasePlan contains signed desired release data. Nomad assigns the
// target job version and modify index only after private staging, so those
// observed coordinates must be zero in this record.
type WorkloadReleasePlan struct {
	SchemaVersion string                `json:"schemaVersion"`
	Intent        WorkloadReleaseIntent `json:"intent"`
}

// WorkloadReleaseStagedTarget is the write-once observation that binds a
// signed plan to the exact target revision returned by the Nomad observer.
type WorkloadReleaseStagedTarget struct {
	SchemaVersion        string                         `json:"schemaVersion"`
	PlanDigest           string                         `json:"planDigest"`
	EffectID             string                         `json:"effectId"`
	TargetJobID          string                         `json:"targetJobId"`
	TargetOperationID    string                         `json:"targetOperationId"`
	TargetExecutionID    string                         `json:"targetExecutionId"`
	TargetJobDigest      string                         `json:"targetJobDigest"`
	TargetJobVersion     uint64                         `json:"targetJobVersion"`
	TargetJobModifyIndex uint64                         `json:"targetJobModifyIndex"`
	Observation          nomad.DeploymentJobObservation `json:"observation"`
	ObservationDigest    string                         `json:"observationDigest"`
	ClaimGeneration      int64                          `json:"claimGeneration"`
}

// Validate checks all desired fields using the existing strict execution
// contract while refusing scheduler coordinates that do not belong in a plan.
func (p WorkloadReleasePlan) Validate() error {
	if p.SchemaVersion != WorkloadReleasePlanSchema || p.Intent.TargetJobVersion != 0 || p.Intent.TargetJobModifyIndex != 0 {
		return fmt.Errorf("workload release plan schema or observed target coordinates are invalid")
	}
	probe := p.Intent
	probe.TargetJobVersion = 1
	probe.TargetJobModifyIndex = 1
	if err := probe.Validate(); err != nil {
		return fmt.Errorf("workload release desired plan is invalid: %w", err)
	}
	return nil
}

// Digest returns the stable digest of the desired plan, which deliberately
// excludes target scheduler coordinates because Validate requires them to be
// absent.
func (p WorkloadReleasePlan) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// BindObservedTarget forms a binding only from an exact successful Nomad
// observation for the plan-derived create-only request. The returned
// version/index remain scheduler observations, never caller-selected fields.
func (p WorkloadReleasePlan) BindObservedTarget(effectID string, claimGeneration int64, request nomad.CASDeploymentJobRequest, observation nomad.DeploymentJobObservation) (WorkloadReleaseStagedTarget, error) {
	planDigest, err := p.Digest()
	if err != nil || strings.TrimSpace(effectID) == "" || claimGeneration <= 0 ||
		request.EffectiveJobID() != p.Intent.TargetJobID || request.Region != p.Intent.NomadRegion ||
		request.Namespace != p.Intent.Namespace || request.DeploymentID != p.Intent.TargetDeploymentID ||
		request.SpecDigest != p.Intent.TargetSpecDigest || request.OperationID != p.Intent.TargetOperationID ||
		request.ExecutionID != p.Intent.TargetExecutionID || strings.TrimPrefix(p.Intent.TargetJobDigest, "sha256:") != request.JobDigest ||
		request.ImageTag != p.Intent.TargetImage || request.ExpectedJobModifyIndex != 0 ||
		observation.State != nomad.DeploymentJobFound || observation.Version == 0 || observation.JobModifyIndex == 0 {
		return WorkloadReleaseStagedTarget{}, fmt.Errorf("workload release target observation is incomplete")
	}
	bound := WorkloadReleaseStagedTarget{
		SchemaVersion: WorkloadReleaseStagedTargetSchema, PlanDigest: planDigest,
		EffectID: effectID, TargetJobID: p.Intent.TargetJobID,
		TargetOperationID: p.Intent.TargetOperationID, TargetExecutionID: p.Intent.TargetExecutionID,
		TargetJobDigest: p.Intent.TargetJobDigest, TargetJobVersion: observation.Version,
		TargetJobModifyIndex: observation.JobModifyIndex, Observation: observation,
		ClaimGeneration: claimGeneration,
	}
	bound.ObservationDigest, err = bound.CanonicalObservationDigest()
	if err != nil {
		return WorkloadReleaseStagedTarget{}, err
	}
	return bound, nil
}

const WorkloadReleaseStagedTargetSchema = "norn.workload-release-staged-target/v1"

// CanonicalObservationDigest binds the scheduler observation to all target
// identities and the accepted plan. It is an integrity digest, not a signature.
func (b WorkloadReleaseStagedTarget) CanonicalObservationDigest() (string, error) {
	if b.SchemaVersion != WorkloadReleaseStagedTargetSchema || !validReleaseDigest(b.PlanDigest) ||
		strings.TrimSpace(b.EffectID) == "" || strings.TrimSpace(b.TargetJobID) == "" ||
		strings.TrimSpace(b.TargetOperationID) == "" || strings.TrimSpace(b.TargetExecutionID) == "" ||
		!validReleaseDigest(b.TargetJobDigest) || b.TargetJobVersion == 0 || b.TargetJobModifyIndex == 0 ||
		b.Observation.State != nomad.DeploymentJobFound || b.Observation.Version != b.TargetJobVersion ||
		b.Observation.JobModifyIndex != b.TargetJobModifyIndex || b.ClaimGeneration <= 0 {
		return "", fmt.Errorf("workload release staged target binding is incomplete")
	}
	projection := struct {
		SchemaVersion        string `json:"schemaVersion"`
		PlanDigest           string `json:"planDigest"`
		EffectID             string `json:"effectId"`
		TargetJobID          string `json:"targetJobId"`
		TargetOperationID    string `json:"targetOperationId"`
		TargetExecutionID    string `json:"targetExecutionId"`
		TargetJobDigest      string `json:"targetJobDigest"`
		TargetJobVersion     uint64 `json:"targetJobVersion"`
		TargetJobModifyIndex uint64 `json:"targetJobModifyIndex"`
		ObservationState     string `json:"observationState"`
		ClaimGeneration      int64  `json:"claimGeneration"`
	}{b.SchemaVersion, b.PlanDigest, b.EffectID, b.TargetJobID, b.TargetOperationID,
		b.TargetExecutionID, b.TargetJobDigest, b.TargetJobVersion, b.TargetJobModifyIndex,
		string(b.Observation.State), b.ClaimGeneration}
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Resolve constructs the strict executable intent only after plan and target
// binding agree. Downstream route, health, and drain code keeps one contract.
func (p WorkloadReleasePlan) Resolve(b WorkloadReleaseStagedTarget, expectedEffectID string, currentClaimGeneration int64) (WorkloadReleaseIntent, error) {
	digest, err := p.Digest()
	if err != nil {
		return WorkloadReleaseIntent{}, err
	}
	observationDigest, err := b.CanonicalObservationDigest()
	if err != nil || strings.TrimSpace(expectedEffectID) == "" || currentClaimGeneration <= 0 ||
		b.EffectID != expectedEffectID || b.ClaimGeneration > currentClaimGeneration ||
		b.ObservationDigest != observationDigest || b.PlanDigest != digest ||
		b.TargetJobID != p.Intent.TargetJobID || b.TargetOperationID != p.Intent.TargetOperationID ||
		b.TargetExecutionID != p.Intent.TargetExecutionID || b.TargetJobDigest != p.Intent.TargetJobDigest {
		return WorkloadReleaseIntent{}, fmt.Errorf("workload release target binding differs from signed plan or observation")
	}
	resolved := p.Intent
	resolved.TargetJobVersion = b.TargetJobVersion
	resolved.TargetJobModifyIndex = b.TargetJobModifyIndex
	if err := resolved.Validate(); err != nil {
		return WorkloadReleaseIntent{}, fmt.Errorf("resolved workload release intent is invalid: %w", err)
	}
	return resolved, nil
}
