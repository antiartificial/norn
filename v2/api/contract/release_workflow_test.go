package contract

import (
	"os"
	"strings"
	"testing"
)

func TestReusableReleaseWorkflowKeepsPrivateEvidenceServerSelectedAndOffArgv(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/norn-app-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		`"scope":"release:attest"`, `.attestationMode`, `/private-attestations`,
		`--data-binary @.norn-release/private-attestation-request.json`,
		`--retry-connrefused --max-time 90`,
		`--data-binary @.norn-release/preflight-request.json`,
		`--data-binary @.norn-release/deployment-request.json`,
		`--data-binary @.norn-release/promotion-request.json`,
		`--slurpfile candidate .norn-release/candidate.json`,
		`if: steps.trust.outputs.mode != 'norn-signed-private'`,
		`Idempotency-Key: norn-private-attestation-$APP_ID-$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT`,
		`Idempotency-Key: norn-staging-preflight-$APP_ID-$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT`,
		`Idempotency-Key: norn-staging-deploy-$APP_ID-$SOURCE_SHA-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	if strings.Contains(workflow, "needs.validate.outputs.attestation_mode") {
		t.Fatal("workflow still derives private trust mode before server OIDC policy exchange")
	}
	if strings.Contains(workflow, `request="$(jq -nc --arg sourceSha "$SOURCE_SHA" --arg artifact "$ARTIFACT" --slurpfile`) {
		t.Fatal("large embedded release evidence is carried in a shell variable/argv")
	}
}
