package pipeline

import (
	"fmt"
	"strings"
	"time"

	"norn/v2/api/model"
)

// ValidateWorkloadReleaseQualification binds a release plan to a current,
// trusted staging qualification for the same application, source revision,
// and content-addressed image. Admission must obtain the qualification from
// the control store; this function verifies its signed contents, not its
// storage provenance.
func (p WorkloadReleasePlan) ValidateWorkloadReleaseQualification(qualification model.ReleaseQualification, trustedKeys []string, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := model.VerifyReleaseQualificationSignature(trustedKeys, qualification); err != nil {
		return fmt.Errorf("workload release qualification signature is invalid: %w", err)
	}
	if qualification.App != p.Intent.App || qualification.Environment != "staging" {
		return fmt.Errorf("workload release qualification does not authorize this app and environment")
	}
	if now.IsZero() || qualification.IssuedAt.IsZero() || qualification.ExpiresAt.IsZero() ||
		qualification.IssuedAt.After(now) || !qualification.ExpiresAt.After(now) || !qualification.ExpiresAt.After(qualification.IssuedAt) {
		return fmt.Errorf("workload release qualification is outside its validity window")
	}
	if qualification.SourceSHA != p.Intent.SourceRevision || qualification.Candidate.Attestation.MaterialSHA != p.Intent.SourceRevision {
		return fmt.Errorf("workload release source differs from the signed staging qualification")
	}
	imageDigest := ""
	if marker := "@sha256:"; strings.Contains(qualification.Artifact, marker) {
		imageDigest = "sha256:" + qualification.Artifact[strings.LastIndex(qualification.Artifact, marker)+len(marker):]
	}
	if qualification.Artifact != p.Intent.TargetImage || !model.IsContentAddressedImage(qualification.Artifact) || imageDigest != p.Intent.TargetImageDigest ||
		qualification.Candidate.Attestation.SubjectDigest != p.Intent.TargetImageDigest {
		return fmt.Errorf("workload release image differs from the signed staging qualification")
	}
	return nil
}
