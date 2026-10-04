package contract

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestMobilityFixturePublicationWorkflowIsManualAndArtifactOnly(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/m7-mobility-fixture-publish.yml")
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
		`[[ "$(git rev-parse origin/master)" == "$REQUESTED_SHA" ]]`,
		"platforms: linux/amd64,linux/arm64",
		"push-by-digest=true,name-canonical=true,push=true",
		"cosign sign --yes",
		"actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d",
		"artifact-metadata: write",
		"sbom-path: publication-evidence/sbom.spdx.json",
		"norn.m7.mobility-fixture.publication-handoff/v1",
		"Repository CI separately builds this Dockerfile",
		"--signer-workflow",
		"--signer-digest ${REVIEWED_WORKFLOW_SHA",
		"--source-digest ${REVIEWED_SOURCE_SHA",
		"--source-ref refs/heads/master",
		"--predicate-type https://slsa.dev/provenance/v1",
		"--predicate-type https://spdx.dev/Document/v2.3",
		"--deny-self-hosted-runners",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("publication workflow is missing %q", required)
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
			t.Errorf("artifact-only publication must not contain %q", forbidden)
		}
	}

	prepareEvidence := strings.Index(workflow, "- name: Prepare publication evidence directory\n        run: mkdir -p publication-evidence")
	generateSBOM := strings.Index(workflow, "- name: Generate SPDX SBOM for the immutable artifact")
	attestProvenance := strings.Index(workflow, "- name: Attest GitHub build provenance for the immutable artifact")
	attestSBOM := strings.Index(workflow, "- name: Attest SPDX SBOM for the immutable artifact")
	writeHandoff := strings.Index(workflow, "- name: Write immutable digest handoff")
	if prepareEvidence < 0 || generateSBOM < 0 || attestProvenance < 0 || attestSBOM < 0 || writeHandoff < 0 ||
		!(prepareEvidence < generateSBOM && generateSBOM < attestProvenance && attestProvenance < attestSBOM && attestSBOM < writeHandoff) {
		t.Error("publication evidence steps must run in preparation, SBOM, provenance, SBOM-attestation, handoff order")
	}
}

func TestMobilityFixturePublicationHandoffGeneratesIndependentVerificationContract(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/m7-mobility-fixture-publish.yml")
	if err != nil {
		t.Fatal(err)
	}

	match := regexp.MustCompile(`(?s)--arg reviewedWorkflowSha "\$REVIEWED_WORKFLOW_SHA".*?--arg sbomURL "\$SBOM_URL"\s*\\\s*'([^']+)'\s*\\\s*> publication-evidence/handoff\.json`).FindStringSubmatch(string(raw))
	if len(match) != 2 {
		t.Fatal("could not find the handoff jq filter in the publication workflow")
	}
	filter := match[1]

	const (
		sourceSHA           = "0123456789abcdef0123456789abcdef01234567"
		artifact            = "ghcr.io/antiartificial/mobility-fixture@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		repository          = "antiartificial/norn"
		signerWorkflow      = "antiartificial/norn/.github/workflows/m7-mobility-fixture-publish.yml"
		reviewedWorkflowSHA = "89abcdef0123456789abcdef0123456789abcdef"
	)
	output, err := exec.Command(
		"jq", "-n",
		"--arg", "sourceSha", sourceSHA,
		"--arg", "artifact", artifact,
		"--arg", "repository", repository,
		"--arg", "signerWorkflow", signerWorkflow,
		"--arg", "reviewedWorkflowSha", reviewedWorkflowSHA,
		"--arg", "provenanceBundleSha256", strings.Repeat("1", 64),
		"--arg", "sbomBundleSha256", strings.Repeat("2", 64),
		"--arg", "provenanceURL", "https://github.com/antiartificial/norn/attestations/one",
		"--arg", "sbomURL", "https://github.com/antiartificial/norn/attestations/two",
		filter,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("handoff jq filter must compile and run: %v: %s", err, output)
	}

	var handoff struct {
		SchemaVersion       string   `json:"schemaVersion"`
		SourceSHA           string   `json:"sourceSha"`
		Artifact            string   `json:"artifact"`
		Repository          string   `json:"repository"`
		SignerWorkflow      string   `json:"signerWorkflow"`
		ReportedWorkflowSHA string   `json:"reportedWorkflowSha"`
		Platforms           []string `json:"platforms"`
		Signature           struct {
			Verification string `json:"verification"`
		} `json:"signature"`
		Verification struct {
			RequiredInputs []string `json:"requiredInputs"`
			Provenance     string   `json:"provenance"`
			SBOM           string   `json:"sbom"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(output, &handoff); err != nil {
		t.Fatalf("handoff jq filter emitted invalid JSON: %v", err)
	}
	if handoff.SchemaVersion != "norn.m7.mobility-fixture.publication-handoff/v1" ||
		handoff.SourceSHA != sourceSHA || handoff.Artifact != artifact ||
		handoff.Repository != repository || handoff.SignerWorkflow != signerWorkflow ||
		handoff.ReportedWorkflowSHA != reviewedWorkflowSHA {
		t.Fatalf("handoff metadata does not preserve representative inputs: %+v", handoff)
	}
	if strings.Join(handoff.Platforms, ",") != "linux/amd64,linux/arm64" {
		t.Fatalf("handoff platforms = %#v", handoff.Platforms)
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
	if !strings.Contains(handoff.Signature.Verification, "cosign verify") || !strings.Contains(handoff.Signature.Verification, artifact) {
		t.Errorf("Cosign verification command = %q", handoff.Signature.Verification)
	}
}
