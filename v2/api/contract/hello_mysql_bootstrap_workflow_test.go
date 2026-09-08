package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
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
		"norn.hello-norn-mysql.bootstrap-handoff/v2",
		"outputs.bundle-path",
		"sha256sum \"$PROVENANCE_BUNDLE\"",
		"sha256sum \"$SBOM_BUNDLE\"",
		"attestationBundleSha256",
		"sbomBundleSha256",
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
	prepareEvidence := strings.Index(workflow, "- name: Prepare bootstrap evidence directory\n        run: mkdir -p bootstrap-evidence")
	generateSBOM := strings.Index(workflow, "- name: Generate SPDX SBOM for the immutable artifact")
	if prepareEvidence < 0 || generateSBOM < 0 || prepareEvidence > generateSBOM {
		t.Error("bootstrap workflow must create bootstrap-evidence before generating the SBOM")
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

func TestHelloNornMySQLBootstrapHandoffJQFilterGeneratesIndependentVerificationContract(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/hello-norn-mysql-bootstrap-image.yml")
	if err != nil {
		t.Fatal(err)
	}

	// Execute the filter embedded in the workflow, rather than a duplicate copy,
	// so a syntactically invalid handoff filter fails this contract test before it
	// can reach the artifact-producing workflow.
	match := regexp.MustCompile(`(?s)--arg verifiedWorkflowSha "\$REVIEWED_WORKFLOW_SHA"\s*\\\s*'([^']+)'\s*\\\s*> bootstrap-evidence/handoff\.json`).FindStringSubmatch(string(raw))
	if len(match) != 2 {
		t.Fatal("could not find the handoff jq filter in the bootstrap workflow")
	}
	filter := match[1]

	const (
		sourceSHA           = "0123456789abcdef0123456789abcdef01234567"
		artifact            = "ghcr.io/antiartificial/hello-norn-mysql@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		repository          = "antiartificial/norn"
		signerWorkflow      = "antiartificial/norn/.github/workflows/hello-norn-mysql-bootstrap-image.yml"
		reviewedWorkflowSHA = "89abcdef0123456789abcdef0123456789abcdef"
	)
	output, err := exec.Command(
		"jq", "-n",
		"--arg", "sourceSha", sourceSHA,
		"--arg", "artifact", artifact,
		"--arg", "repository", repository,
		"--arg", "signerWorkflow", signerWorkflow,
		"--arg", "verifiedWorkflowSha", reviewedWorkflowSHA,
		filter,
	).Output()
	if err != nil {
		t.Fatalf("handoff jq filter must compile and run: %v", err)
	}

	var handoff struct {
		SchemaVersion       string `json:"schemaVersion"`
		SourceSHA           string `json:"sourceSha"`
		Artifact            string `json:"artifact"`
		Repository          string `json:"repository"`
		SignerWorkflow      string `json:"signerWorkflow"`
		ReportedWorkflowSHA string `json:"reportedWorkflowSha"`
		Verification        struct {
			RequiredInputs []string `json:"requiredInputs"`
			Provenance     string   `json:"provenance"`
			SBOM           string   `json:"sbom"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(output, &handoff); err != nil {
		t.Fatalf("handoff jq filter emitted invalid JSON: %v", err)
	}
	if handoff.SchemaVersion != "norn.hello-norn-mysql.bootstrap-handoff/v1" ||
		handoff.SourceSHA != sourceSHA || handoff.Artifact != artifact ||
		handoff.Repository != repository || handoff.SignerWorkflow != signerWorkflow ||
		handoff.ReportedWorkflowSHA != reviewedWorkflowSHA {
		t.Fatalf("handoff metadata does not preserve representative inputs: %+v", handoff)
	}
	if strings.Join(handoff.Verification.RequiredInputs, ",") != "REVIEWED_SOURCE_SHA,REVIEWED_WORKFLOW_SHA" {
		t.Fatalf("handoff verification required inputs = %#v", handoff.Verification.RequiredInputs)
	}

	wantCommand := func(predicateType string) string {
		return "gh attestation verify oci://" + artifact +
			" --repo " + repository +
			" --signer-workflow " + signerWorkflow +
			" --signer-digest ${REVIEWED_WORKFLOW_SHA:?set from independently reviewed policy}" +
			" --source-digest ${REVIEWED_SOURCE_SHA:?set from independently reviewed policy}" +
			" --source-ref refs/heads/master --predicate-type " + predicateType +
			" --deny-self-hosted-runners"
	}
	if handoff.Verification.Provenance != wantCommand("https://slsa.dev/provenance/v1") {
		t.Errorf("provenance verification command = %q", handoff.Verification.Provenance)
	}
	if handoff.Verification.SBOM != wantCommand("https://spdx.dev/Document/v2.3") {
		t.Errorf("SBOM verification command = %q", handoff.Verification.SBOM)
	}
}
