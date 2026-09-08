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

func TestHelloNornMySQLReleaseCallerIsPinnedAndWorkloadScoped(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/hello-norn-mysql-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		`branches: [master]`,
		`tags: ["v*"]`,
		`- "v2/infra/fleet-pilot/hello-norn-mysql/**"`,
		`github.ref == 'refs/heads/master' && github.ref_protected`,
		`startsWith(github.ref, 'refs/tags/v') && github.ref_protected`,
		`antiartificial/norn/.github/workflows/norn-app-release.yml@1d3f5802b5554e06ea1593146f00402d00ca9b2b`,
		`app_id: hello-norn-mysql`,
		`image_repository: ghcr.io/antiartificial/hello-norn-mysql`,
		`build_contract: pilot-go`,
		`go_image: golang@sha256:484ef6066fa69acb059fdfeda7ba2b8f7391f2ef6abc6f9b8411e669ebd56466`,
		`runtime_image: gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`,
		"Fleet pilot workload\t15368",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("caller workflow is missing %q", required)
		}
	}
	if strings.Contains(workflow, ".github/workflows/hello-norn-mysql-release.yml\"") {
		t.Fatal("caller workflow path would trigger staging when the caller itself merges")
	}
}
