package contract

import (
	"os"
	"strings"
	"testing"
)

func TestHelloNornMySQLBootstrapWorkflowIsArtifactOnlyAndBoundToProtectedMaster(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/hello-norn-mysql-bootstrap-image.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		"workflow_dispatch:",
		"commit_sha:",
		"reviewed_workflow_sha:",
		`[[ "$GITHUB_REF" == "refs/heads/master" ]]`,
		`[[ "$REF_PROTECTED" == "true" ]]`,
		`[[ "$REQUESTED_SHA" == "$GITHUB_SHA" ]]`,
		`[[ "$REVIEWED_WORKFLOW_SHA" == "$GITHUB_WORKFLOW_SHA" ]]`,
		`git merge-base --is-ancestor "$REQUESTED_SHA" origin/master`,
		"push-by-digest=true,name-canonical=true,push=true",
		"GO_IMAGE=golang@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466",
		"RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab",
		"actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d",
		"artifact-metadata: write",
		"sbom-path: bootstrap-evidence/sbom.spdx.json",
		"norn.hello-norn-mysql.bootstrap-handoff/v1",
		"oci://",
		"--predicate-type https://slsa.dev/provenance/v1",
		"--predicate-type https://spdx.dev/Document/v2.3",
		"--signer-workflow",
		"--signer-digest ${REVIEWED_WORKFLOW_SHA",
		"--source-digest ${REVIEWED_SOURCE_SHA",
		"--source-ref refs/heads/master",
		"--deny-self-hosted-runners",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("bootstrap workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"NORN_API",
		"tailscale",
		"norn-app-release.yml",
		"hello-norn-mysql:bootstrap-",
		"release:stage",
		"release:qualify",
		"release:promote",
		"/api/v1/",
		"curl ",
	} {
		if strings.Contains(strings.ToLower(workflow), strings.ToLower(forbidden)) {
			t.Errorf("artifact-only bootstrap must not contain %q", forbidden)
		}
	}
}

func TestHelloNornMySQLBootstrapDocsRequireIndependentVerificationPolicy(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/v2/operations/hello-norn-mysql-bootstrap.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, required := range []string{
		"handoff is discovery material only; it is not policy",
		"REVIEWED_SOURCE_SHA",
		"REVIEWED_WORKFLOW_SHA",
		"oci://$artifact",
		"--signer-workflow",
		"--signer-digest",
		"--source-digest",
		"--source-ref refs/heads/master",
		"--predicate-type https://slsa.dev/provenance/v1",
		"--predicate-type https://spdx.dev/Document/v2.3",
		"--deny-self-hosted-runners",
	} {
		if !strings.Contains(doc, required) {
			t.Errorf("bootstrap documentation is missing %q", required)
		}
	}
}

func TestHelloNornMySQLBootstrapFailsClosedForNonPublicRepositories(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/hello-norn-mysql-bootstrap-image.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		`visibility="$(gh api "repos/$GITHUB_REPOSITORY" --jq .visibility)"`,
		`[[ "$visibility" == public ]]`,
		"refuses $visibility repositories",
		"independently operated attestation signer",
		"Repository visibility changed to $visibility",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("private/internal fail-closed contract is missing %q", required)
		}
	}
}
