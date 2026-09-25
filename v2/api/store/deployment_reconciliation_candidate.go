package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"norn/v2/api/model"
)

var ErrDeploymentReconciliationUnavailable = errors.New("deployment reconciliation candidate is unavailable")

// DeploymentReconciliationCandidate is a verified source for a later operator
// reconciliation. It does not authorize a mutation or assert Nomad health.
type DeploymentReconciliationCandidate struct {
	Acceptance AcceptedOperation
	ImageTag   string
}

// DeploymentReconciliationCandidate loads immutable acceptance and the exact
// image identity of a failed, lease-expired mutable deployment. A newer
// deployment makes the candidate stale even if the old Nomad job still runs.
func (s *PGOperationStore) DeploymentReconciliationCandidate(ctx context.Context, operationID string) (DeploymentReconciliationCandidate, error) {
	accepted, err := s.VerifyAcceptedOperation(ctx, operationID)
	if err != nil {
		return DeploymentReconciliationCandidate{}, err
	}
	op, d := accepted.Operation, accepted.Deployment
	if (op.Kind != "app.deploy" && op.Kind != "app.rollback") || op.Status != model.OperationFailed ||
		op.LastError != "operation executor lease expired" || op.Metadata["manualRecoveryRequired"] != true ||
		d == nil || d.Status != model.StatusFailed || d.App != op.App || d.SagaID != op.SagaID ||
		d.SpecDigest == "" || d.SpecDigest != stringFromAcceptedPayload(op.Payload, "specDigest") || len(accepted.Regions) == 0 {
		return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
	}
	var superseded bool
	if err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM deployments WHERE app=$1 AND environment=$2 AND id<>$3 AND started_at >= $4
	)`, d.App, d.Environment, d.ID, d.StartedAt).Scan(&superseded); err != nil {
		return DeploymentReconciliationCandidate{}, err
	}
	if superseded {
		return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
	}
	image := d.ImageTag
	if op.Kind == "app.deploy" {
		checkpoint, err := s.db.LoadOperationCheckpoint(ctx, operationID, CheckpointBuild)
		if err != nil {
			return DeploymentReconciliationCandidate{}, err
		}
		if checkpoint != nil {
			source, err := s.db.LoadOperationCheckpoint(ctx, operationID, CheckpointSource)
			if err != nil {
				return DeploymentReconciliationCandidate{}, err
			}
			var build struct {
				ImageTag       string `json:"imageTag"`
				SourceIdentity string `json:"sourceIdentity"`
			}
			if err := json.Unmarshal(checkpoint.Outputs, &build); err != nil || source == nil || build.SourceIdentity != checkpointDigest(source.Outputs) || !model.IsContentAddressedImage(build.ImageTag) {
				return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
			}
			if image != "" && image != build.ImageTag {
				return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
			}
			image = build.ImageTag
		}
	} else if image != stringFromAcceptedPayload(op.Payload, "imageTag") {
		return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
	}
	if !model.IsContentAddressedImage(image) {
		return DeploymentReconciliationCandidate{}, ErrDeploymentReconciliationUnavailable
	}
	return DeploymentReconciliationCandidate{Acceptance: accepted, ImageTag: image}, nil
}

func stringFromAcceptedPayload(payload map[string]interface{}, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
