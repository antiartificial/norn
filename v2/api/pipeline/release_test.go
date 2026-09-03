package pipeline

import (
	"context"
	"strings"
	"testing"

	"norn/v2/api/model"
)

func TestReleaseBindingAdmissionRejectsQueuedSpecMutation(t *testing.T) {
	artifact := "ghcr.io/acme/orders-api@sha256:" + strings.Repeat("a", 64)
	candidate := queuedReleaseCandidate("acme/orders-api", artifact)
	pipeline := &Pipeline{RegistryURL: "ghcr.io/acme"}
	state := &state{
		spec:          &model.InfraSpec{App: "orders-api", Repo: &model.RepoSpec{URL: "https://github.com/acme/orders-api.git"}, Build: &model.BuildSpec{Dockerfile: "Dockerfile"}},
		imageTag:      artifact,
		artifactBound: true,
		candidate:     candidate,
	}
	if err := pipeline.releaseBindingAdmission(context.Background(), state, nil); err != nil {
		t.Fatalf("unchanged queued release binding rejected: %v", err)
	}

	state.spec.Repo.URL = "git@github.com:acme/billing-api.git"
	if err := pipeline.releaseBindingAdmission(context.Background(), state, nil); err == nil {
		t.Fatal("worker accepted a release after its server-owned source changed while queued")
	}
	state.spec.Repo.URL = "https://github.com/acme/orders-api.git"
	state.spec.Build.Image = "ghcr.io/acme/billing-api@sha256:" + strings.Repeat("b", 64)
	if err := pipeline.releaseBindingAdmission(context.Background(), state, nil); err == nil {
		t.Fatal("worker accepted a release after its server-owned artifact policy changed while queued")
	}
}

func TestReleaseOperationCandidateFailsClosedWithoutReleaseEvidence(t *testing.T) {
	artifact := "ghcr.io/acme/orders-api@sha256:" + strings.Repeat("a", 64)
	valid := model.Operation{Source: "release-control-api", Metadata: map[string]interface{}{"candidate": queuedReleaseCandidate("acme/orders-api", artifact)}}
	if candidate, err := releaseOperationCandidate(&valid); err != nil || candidate.Repository != "acme/orders-api" {
		t.Fatalf("valid release candidate = %#v, %v", candidate, err)
	}
	for name, operation := range map[string]model.Operation{
		"missing":   {Source: "release-control-api"},
		"malformed": {Source: "release-control-api", Metadata: map[string]interface{}{"candidate": "not a candidate"}},
		"incomplete": {Source: "release-control-api", Metadata: map[string]interface{}{
			"candidate": model.ReleaseCandidate{Provider: "github-actions", Repository: "acme/orders-api"},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := releaseOperationCandidate(&operation); err == nil {
				t.Fatalf("%s release operation fell back to legacy candidate handling", name)
			}
		})
	}
	legacy := model.Operation{Source: "pipeline"}
	if candidate, err := releaseOperationCandidate(&legacy); err != nil || candidate.Provider != "" {
		t.Fatalf("legacy operation was not preserved: %#v, %v", candidate, err)
	}
}

func queuedReleaseCandidate(repository, artifact string) model.ReleaseCandidate {
	sha := strings.Repeat("b", 40)
	return model.ReleaseCandidate{
		Provider: "github-actions", Repository: repository, RepositoryID: "101", OwnerID: "202", RepositoryVisibility: "private", RunID: "303", RunAttempt: "1",
		WorkflowRef: repository + "/.github/workflows/release.yml@" + sha, WorkflowSHA: sha,
		SignerWorkflowRef: "antiartificial/norn/.github/workflows/norn-app-release.yml@" + sha, SignerWorkflowSHA: sha, Ref: "refs/heads/main",
		Attestation: model.ReleaseAttestationIdentity{Mode: "github-private", Verifier: "GitHub private Sigstore", Issuer: "https://token.actions.githubusercontent.com", SubjectDigest: "sha256:" + strings.Repeat("a", 64), MaterialSHA: sha},
	}
}
