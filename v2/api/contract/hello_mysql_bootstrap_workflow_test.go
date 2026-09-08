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
		`[[ "$GITHUB_REF" == "refs/heads/master" ]]`,
		`[[ "$REF_PROTECTED" == "true" ]]`,
		`[[ "$REQUESTED_SHA" == "$GITHUB_SHA" ]]`,
		`git merge-base --is-ancestor "$REQUESTED_SHA" origin/master`,
		"ghcr.io/antiartificial/hello-norn-mysql:bootstrap-${{ needs.authorize.outputs.source_sha }}",
		"immutable bootstrap tag already exists",
		"GO_IMAGE=golang@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466",
		"RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab",
		"actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d",
		"sbom-path: bootstrap-evidence/sbom.spdx.json",
		"norn.hello-norn-mysql.bootstrap-handoff/v1",
		"gh attestation verify --repo",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("bootstrap workflow is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"NORN_API",
		"tailscale",
		"norn-app-release.yml",
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
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("private/internal fail-closed contract is missing %q", required)
		}
	}
}
