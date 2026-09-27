package etcdstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"norn/v2/api/database"
	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// V3DeploymentEffectReservations uses the same unresolved app gate and effect
// token lifecycle as canary promotion, with a distinct deploy namespace and
// a validator bound to the signed deployment and accepted placement.
type V3DeploymentEffectReservations struct{ *V3CanaryEffectReservations }

var (
	_ effect.Store         = (*V3DeploymentEffectReservations)(nil)
	_ effect.RecoveryStore = (*V3DeploymentEffectReservations)(nil)
)

func NewV3DeploymentEffectReservations(operations *V3OperationStore) (*V3DeploymentEffectReservations, error) {
	if operations == nil || operations.kv == nil || operations.lease == nil || operations.authority == "" {
		return nil, fmt.Errorf("etcd deployment effects require a v3 operation store")
	}
	return &V3DeploymentEffectReservations{&V3CanaryEffectReservations{operations: operations, namespace: "deploy"}}, nil
}

type deploymentEffectInput = nomad.DeploymentJobEffectInput

func deploymentEffectExecutionID(operationID, region, jobDigest string) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + region + "\x00" + jobDigest))
	return "nomad-deployment-" + hex.EncodeToString(sum[:16])
}

func (s *V3CanaryEffectReservations) validateDeploymentEffectReservation(ctx context.Context, r effect.Reservation, op model.Operation) (string, error) {
	if r.Authority != s.operations.authority || r.OperationClaim.OperationID != op.ID || r.OperationClaim.OwnerID == "" || r.OperationClaim.Generation <= 0 ||
		r.Stage != "app.deploy.nomad.submit" || r.Supervisor != "nomad-deployment" || op.Kind != "app.deploy" {
		return "", fmt.Errorf("deployment effect reservation does not match accepted operation and authority")
	}
	var input deploymentEffectInput
	if err := json.Unmarshal(r.LaunchPayload, &input); err != nil {
		return "", fmt.Errorf("decode deployment effect input: %w", err)
	}
	jobDigest, err := hex.DecodeString(input.JobDigest)
	if err != nil || len(jobDigest) != sha256.Size || hex.EncodeToString(jobDigest) != input.JobDigest ||
		input.App == "" || input.DeploymentID == "" || input.Region == "" || input.NomadRegion == "" || input.ImageTag == "" || input.SpecDigest == "" ||
		strings.Contains(input.App, "/") || strings.Contains(input.Region, "/") ||
		input.App != op.App || input.DeploymentID != effectPayloadString(op.Payload, "deploymentId") ||
		r.Resource != "app/"+input.App+"/deploy/"+input.Region ||
		r.SupervisorExecutionID != deploymentEffectExecutionID(op.ID, input.Region, input.JobDigest) {
		return "", fmt.Errorf("deployment effect input is incomplete or differs from accepted operation")
	}
	index, err := s.operations.kv.Get(ctx, s.operations.operationAcceptanceIndexKey(op.ID))
	if err != nil {
		return "", err
	}
	if len(index.Kvs) != 1 {
		return "", fmt.Errorf("deployment effect has no signed acceptance index")
	}
	key := string(index.Kvs[0].Value)
	acceptedRecord, err := s.operations.loadAcceptance(ctx, key)
	if err != nil || acceptedRecord.record.Accepted.Operation.ID != op.ID || s.operations.acceptanceKey(acceptedRecord.record.Identity) != key {
		return "", fmt.Errorf("deployment effect acceptance link is invalid")
	}
	accepted, err := s.operations.replay(ctx, key, acceptedRecord, acceptedRecord.record.Identity, acceptedRecord.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return "", err
	}
	if accepted.Deployment == nil || accepted.Deployment.ID != input.DeploymentID || accepted.Deployment.App != input.App ||
		accepted.Deployment.ImageTag != input.ImageTag || accepted.Deployment.SpecDigest != input.SpecDigest {
		return "", fmt.Errorf("deployment effect artifact differs from signed deployment")
	}
	regionFound := false
	for _, region := range accepted.Regions {
		if region.Name == input.Region && region.NomadRegion == input.NomadRegion {
			regionFound = true
			break
		}
	}
	if !regionFound {
		return "", fmt.Errorf("deployment effect region is not accepted")
	}
	if err := validateManagedEffectTargets(input, accepted.Operation.Payload); err != nil {
		return "", err
	}
	digest, err := effect.ComputeInputDigest(r)
	if err != nil || r.InputDigest != digest {
		return "", fmt.Errorf("deployment effect digest differs from reservation")
	}
	return input.App, nil
}

func validateManagedEffectTargets(input deploymentEffectInput, payload map[string]interface{}) error {
	if input.JobID == "" {
		if input.ManagedInputs != nil || len(input.ExpectedDatabaseTargets) != 0 {
			return fmt.Errorf("legacy deployment has managed input fields")
		}
		return nil
	}
	wantID, err := nomad.ManagedDeploymentJobID(input.App, input.Region, input.DeploymentID)
	if err != nil || input.JobID != wantID || input.ManagedInputs == nil || input.ManagedInputs.JobID != wantID ||
		input.ManagedInputs.VariablePath != nomad.DatabaseVariablePath(wantID) || input.ExpectedJobModifyIndex != 0 {
		return fmt.Errorf("managed deployment job identity differs from accepted work")
	}
	plan := input.ManagedInputs
	if len(plan.RuntimeDatabaseNames) == 0 {
		if len(input.ExpectedDatabaseTargets) != 0 {
			return fmt.Errorf("managed deployment has unexpected database targets")
		}
		return nil
	}
	raw, ok := payload["databaseTargets"].(string)
	if !ok {
		return fmt.Errorf("managed deployment has no signed database target set")
	}
	var signed struct {
		Schema          string `json:"schema"`
		ProfileID       string `json:"profileId"`
		CatalogRevision int64  `json:"catalogRevision"`
		Targets         []struct {
			Name   string                  `json:"name"`
			Target database.TargetIdentity `json:"target"`
		} `json:"targets"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var trailing json.RawMessage
	if decoder.Decode(&signed) != nil || decoder.Decode(&trailing) != io.EOF || signed.Schema != "norn.database-targets/v1" ||
		signed.CatalogRevision < 1 || signed.CatalogRevision != plan.DatabaseRevision || len(signed.Targets) < len(plan.RuntimeDatabaseNames) ||
		len(input.ExpectedDatabaseTargets) != len(plan.RuntimeDatabaseNames) {
		return fmt.Errorf("managed deployment signed database targets differ")
	}
	known := map[string]string{}
	for _, entry := range signed.Targets {
		if entry.Name == "" || known[entry.Name] != "" {
			return fmt.Errorf("managed deployment signed database targets are ambiguous")
		}
		encoded, err := json.Marshal(entry.Target)
		if err != nil {
			return fmt.Errorf("managed deployment signed database target is invalid")
		}
		known[entry.Name] = string(encoded)
	}
	for _, name := range plan.RuntimeDatabaseNames {
		if known[name] == "" || input.ExpectedDatabaseTargets[name] != known[name] {
			return fmt.Errorf("managed deployment target %s differs from signed acceptance", name)
		}
	}
	return nil
}

// The normal etcd Fleet router still refuses deployment admission. This
// reservation alone cannot authorize Nomad submission; its supervisor must
// verify the exact job digest and durable execution namespace before launch.
