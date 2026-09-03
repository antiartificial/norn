package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/model"
	"norn/v2/api/saga"
)

func (p *Pipeline) VerifyReleaseArtifact(ctx context.Context, spec *model.InfraSpec, sourceSHA, artifact string, candidate model.ReleaseCandidate) error {
	if spec == nil {
		return fmt.Errorf("standalone release verification requires the server-owned app spec")
	}
	return p.verifyReleaseArtifactAdmission(ctx, &state{spec: spec, commitSHA: sourceSHA, imageTag: artifact, artifactBound: true, candidate: candidate})
}

// releaseBindingAdmission repeats the server-owned source and OCI namespace
// binding at worker time. An operation can wait behind another deploy, during
// which an InfraSpec change must not redirect already-approved CI evidence.
func (p *Pipeline) releaseBindingAdmission(_ context.Context, st *state, _ *saga.Saga) error {
	if st == nil || st.candidate.Provider == "" {
		return nil // legacy deployment: no release candidate was queued.
	}
	if !st.artifactBound {
		return fmt.Errorf("release candidate has no immutable artifact")
	}
	if err := model.ValidateReleaseSpecBinding(st.spec, st.candidate, st.imageTag, p.RegistryURL); err != nil {
		return fmt.Errorf("release source/artifact binding changed while queued: %w", err)
	}
	return nil
}

func (p *Pipeline) QueueReleaseDeployment(ctx context.Context, spec *model.InfraSpec, sourceSHA, artifact, environment string, metadata map[string]interface{}) (*model.Operation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil {
		return nil, fmt.Errorf("release deployment pipeline is unavailable")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	now := time.Now().UTC()
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "deploy")
	deployment := &model.Deployment{ID: uuid.NewString(), App: spec.App, CommitSHA: sourceSHA, ImageTag: artifact, Environment: environment, SagaID: sg.ID, Status: model.StatusQueued, SourceRef: sourceSHA, StartedAt: now}
	metadata["sourceSha"], metadata["artifact"], metadata["environment"] = sourceSHA, artifact, environment
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.deploy", App: spec.App, SagaID: sg.ID, Ref: sourceSHA, Status: model.OperationQueued, Risk: "app rolling update", Source: "release-control-api", Message: "queued release deployment", StartedAt: now, MaxAttempts: 2, Payload: map[string]interface{}{"deploymentId": deployment.ID, "app": spec.App, "sourceSha": sourceSHA, "artifact": artifact, "candidate": metadata["candidate"]}, Metadata: metadata}
	if err := p.DB.InsertDeploymentOperation(ctx, deployment, spec.ResolvedRegions(), op); err != nil {
		return nil, err
	}
	return op, nil
}

func (p *Pipeline) QueueReleasePreflight(ctx context.Context, spec *model.InfraSpec, sourceSHA, artifact, environment string, metadata map[string]interface{}) (*model.Operation, error) {
	if p == nil || p.DB == nil || p.SagaStore == nil || spec == nil {
		return nil, fmt.Errorf("release preflight pipeline is unavailable")
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	now := time.Now().UTC()
	sg := saga.New(p.SagaStore, spec.App, "pipeline", "preflight")
	metadata["sourceSha"], metadata["artifact"], metadata["environment"] = sourceSHA, artifact, environment
	op := &model.Operation{ID: uuid.NewString(), Kind: "app.preflight", App: spec.App, SagaID: sg.ID, Ref: sourceSHA, Status: model.OperationQueued, Risk: "read-only", Source: "release-control-api", Message: "queued release preflight", StartedAt: now, MaxAttempts: 2, Payload: map[string]interface{}{"app": spec.App, "sourceSha": sourceSHA, "artifact": artifact, "candidate": metadata["candidate"]}, Metadata: metadata}
	if err := p.DB.InsertOperation(ctx, op); err != nil {
		return nil, err
	}
	return op, nil
}
