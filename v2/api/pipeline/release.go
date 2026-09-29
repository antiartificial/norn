package pipeline

import (
	"context"
	"fmt"

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
