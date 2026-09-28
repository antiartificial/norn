package handler

import (
	"fmt"
	"strings"

	"norn/v2/api/model"
)

// BindFleetStagingReleaseCandidate derives the first Fleet release candidate
// from a verified managed GitHub Actions token. Only portable evidence fields
// are copied from the request; its claimed identity is never authoritative.
// The artifact verifier must still inspect the image and attestation before
// the resulting candidate can be accepted.
func BindFleetStagingReleaseCandidate(principal AccessPrincipal, app, defaultBranch, sourceSHA, artifact,
	trustMode string, supplied model.ReleaseCandidate) (model.ReleaseCandidate, error) {
	ci := principal.CI
	if principal.Source != AccessPrincipalSourceManagedToken || ci == nil || !principal.Allows(ScopeReleaseStage) ||
		app == "" || principal.App != app || principal.Environment != "staging" || ci.Environment != "staging" ||
		ci.Intent != "stage" || !ci.RefProtected || ci.EventName != "push" ||
		strings.TrimSpace(defaultBranch) == "" || ci.Ref != "refs/heads/"+strings.TrimSpace(defaultBranch) {
		return model.ReleaseCandidate{}, fmt.Errorf("Fleet staging release requires a protected, app-bound GitHub Actions stage identity")
	}
	if !validReleaseProvenance(sourceSHA, artifact) || artifact == "" || ci.SHA != sourceSHA {
		return model.ReleaseCandidate{}, fmt.Errorf("Fleet staging release source and artifact do not match verified CI")
	}
	candidate := releaseCandidateFromCI(*ci, sourceSHA, artifact, trustMode)
	candidate.Attestation.Bundle = supplied.Attestation.Bundle
	candidate.Attestation.ProvenanceURI = supplied.Attestation.ProvenanceURI
	candidate.Attestation.SBOMURI = supplied.Attestation.SBOMURI
	if !validReleaseCandidateForTrust(candidate, sourceSHA, artifact, trustMode) || !releaseCandidateMatchesPrincipal(candidate, principal) {
		return model.ReleaseCandidate{}, fmt.Errorf("Fleet staging release candidate does not match verified CI and trust mode")
	}
	return candidate, nil
}
