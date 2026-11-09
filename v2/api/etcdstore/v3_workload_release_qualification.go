package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

// ResolveWorkloadReleaseQualification loads one server-selected qualification
// by ID, verifies its signed V3 operation record and staging signature, and
// confirms that its source deployment completed in this control plane. It is
// a building block for authenticated admission; callers still bind the result
// to the accepted plan and current serving lineage transaction.
func (s *V3OperationStore) ResolveWorkloadReleaseQualification(ctx context.Context, app, qualificationID string, trustedKeys []string, now time.Time) (model.ReleaseQualification, error) {
	if s == nil || s.kv == nil || app == "" || qualificationID == "" || len(trustedKeys) == 0 || now.IsZero() {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification lookup is incomplete")
	}
	index, err := s.kv.Get(ctx, s.operationAcceptanceIndexKey(qualificationID))
	if err != nil {
		return model.ReleaseQualification{}, err
	}
	if len(index.Kvs) != 1 {
		return model.ReleaseQualification{}, ErrNotFound
	}
	key := string(index.Kvs[0].Value)
	loaded, err := s.loadAcceptance(ctx, key)
	if err != nil {
		return model.ReleaseQualification{}, err
	}
	acceptedOperation := loaded.record.Accepted.Operation
	if acceptedOperation.ID != qualificationID || acceptedOperation.Kind != "release.qualification" ||
		acceptedOperation.Status != model.OperationSucceeded || acceptedOperation.App != app ||
		s.acceptanceKey(loaded.record.Identity) != key {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification operation is not an accepted successful qualification for this app")
	}
	accepted, err := s.replay(ctx, key, loaded, loaded.record.Identity, loaded.record.Accepted.Intent.Fingerprint)
	if err != nil {
		return model.ReleaseQualification{}, err
	}
	if accepted.Operation.ID != qualificationID || accepted.Operation.Status != model.OperationSucceeded || accepted.Operation.App != app {
		return model.ReleaseQualification{}, &store.AcceptanceSignatureError{Err: fmt.Errorf("workload release qualification operation projection differs from signed acceptance")}
	}
	encoded, err := json.Marshal(accepted.Operation.Payload)
	if err != nil {
		return model.ReleaseQualification{}, err
	}
	var qualification model.ReleaseQualification
	if err := json.Unmarshal(encoded, &qualification); err != nil || qualification.SchemaVersion != model.ReleaseQualificationSchema || qualification.ID != qualificationID ||
		qualification.App != app || qualification.Environment != "staging" {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification payload is invalid")
	}
	if err := model.VerifyReleaseQualificationSignature(trustedKeys, qualification); err != nil {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification signature is invalid: %w", err)
	}
	if qualification.IssuedAt.IsZero() || qualification.IssuedAt.After(now) || qualification.ExpiresAt.IsZero() ||
		!qualification.ExpiresAt.After(now) || !qualification.ExpiresAt.After(qualification.IssuedAt) {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification is outside its validity window")
	}
	deployment, err := s.GetDeployment(ctx, qualification.DeploymentID)
	if err != nil || deployment == nil || deployment.Status != model.StatusDeployed || deployment.App != app ||
		deployment.Environment != "staging" || deployment.SourceKind != "release" ||
		deployment.CommitSHA != qualification.SourceSHA || deployment.ImageTag != qualification.Artifact || len(deployment.Regions) == 0 {
		return model.ReleaseQualification{}, fmt.Errorf("workload release qualification has no matching successful staging deployment")
	}
	for _, region := range deployment.Regions {
		if region.Status != model.StatusDeployed || region.ActiveWeight != region.DesiredWeight {
			return model.ReleaseQualification{}, fmt.Errorf("workload release qualification deployment has an incomplete region")
		}
	}
	return qualification, nil
}

var _ interface {
	ResolveWorkloadReleaseQualification(context.Context, string, string, []string, time.Time) (model.ReleaseQualification, error)
} = (*V3OperationStore)(nil)
