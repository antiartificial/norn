package contract

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseWorkflow struct {
	Jobs map[string]struct {
		Steps []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

type releaseCallerWorkflow struct {
	Jobs map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
	} `yaml:"jobs"`
}

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

func TestReusableReleaseWorkflowSupportsPrivateTailscaleAPIs(t *testing.T) {
	raw, err := os.ReadFile("../../../.github/workflows/norn-app-release.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(raw)
	for _, required := range []string{
		`private_network:`,
		`tailscale_target:`,
		`private NORN_API_URL must be the exact pathless tailscale_target on HTTPS port 443`,
		`tailscale/github-action@6cae46e2d796f265265cfcf628b72a32b4d7cade # v3.3.0`,
		`oauth-client-id: ${{ secrets.NORN_TAILSCALE_OAUTH_CLIENT_ID }}`,
		`oauth-secret: ${{ secrets.NORN_TAILSCALE_OAUTH_SECRET }}`,
		`tags: tag:norn-release-staging-ci`,
		`tags: tag:norn-release-production-ci`,
		`version: 1.102.3`,
		`sha256sum: 36ddd9b51be57ffc2990cf76323cfa13643bfbb1b8a969f6183fa164741cdef5`,
		`hostname: norn-ci-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}`,
		`targets: ${{ inputs.tailscale_target }}`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("private release route is missing %q", required)
		}
	}
	if got := strings.Count(workflow, `uses: tailscale/github-action@6cae46e2d796f265265cfcf628b72a32b4d7cade`); got != 4 {
		t.Fatalf("private release route joins = %d, want exactly staging, production, requalify, and rollback", got)
	}
	for value, want := range map[string]int{
		`sha256sum: 36ddd9b51be57ffc2990cf76323cfa13643bfbb1b8a969f6183fa164741cdef5`:         4,
		`hostname: norn-ci-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}`: 4,
		`tags: tag:norn-release-staging-ci`:                                                   2,
		`tags: tag:norn-release-production-ci`:                                                2,
	} {
		if got := strings.Count(workflow, value); got != want {
			t.Errorf("private release route contains %q %d times, want %d", value, got, want)
		}
	}
	if strings.Contains(workflow, `authkey:`) || strings.Contains(workflow, `secrets: inherit`) {
		t.Fatal("private release route must use environment-scoped OAuth credentials without caller secret inheritance")
	}

	var parsed releaseWorkflow
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse release workflow: %v", err)
	}
	lanes := map[string]struct {
		joinName     string
		firstRequest string
		tag          string
	}{
		"staging":    {"Join the private staging API route", "Resolve the server-owned attestation policy", "tag:norn-release-staging-ci"},
		"production": {"Join the private production API route", "Queue the immutable production promotion", "tag:norn-release-production-ci"},
		"requalify":  {"Join the private staging API route", "Issue fresh evidence for the existing staging deployment", "tag:norn-release-staging-ci"},
		"rollback":   {"Join the private production API route", "Queue and wait for the exact Norn rollback", "tag:norn-release-production-ci"},
	}
	for lane, expectation := range lanes {
		job, ok := parsed.Jobs[lane]
		if !ok {
			t.Errorf("release workflow is missing %s job", lane)
			continue
		}
		joinIndex, requestIndex := -1, -1
		for i, step := range job.Steps {
			switch step.Name {
			case expectation.joinName:
				joinIndex = i
				if step.Uses != "tailscale/github-action@6cae46e2d796f265265cfcf628b72a32b4d7cade" {
					t.Errorf("%s private route uses %q", lane, step.Uses)
				}
				wantWith := map[string]string{
					"tags":      expectation.tag,
					"version":   "1.102.3",
					"sha256sum": "36ddd9b51be57ffc2990cf76323cfa13643bfbb1b8a969f6183fa164741cdef5",
					"hostname":  "norn-ci-${{ inputs.lane }}-${{ github.run_id }}-${{ github.run_attempt }}",
					"targets":   "${{ inputs.tailscale_target }}",
				}
				for key, want := range wantWith {
					if got := step.With[key]; got != want {
						t.Errorf("%s private route %s = %q, want %q", lane, key, got, want)
					}
				}
			case expectation.firstRequest:
				requestIndex = i
			}
		}
		if joinIndex < 0 || requestIndex < 0 || joinIndex > requestIndex {
			t.Errorf("%s must join its private route before its first Norn API request", lane)
		}
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

	var caller releaseCallerWorkflow
	if err := yaml.Unmarshal(raw, &caller); err != nil {
		t.Fatalf("parse release caller workflow: %v", err)
	}
	// GitHub validates the permissions of every job in a reusable workflow,
	// even when the lane input makes some of them ineligible to run. These are
	// the exact union requested by the immutable norn-app-release.yml pin.
	wantPermissions := map[string]string{
		"actions":           "read",
		"artifact-metadata": "write",
		"attestations":      "write",
		"checks":            "read",
		"contents":          "read",
		"deployments":       "write",
		"id-token":          "write",
		"packages":          "write",
	}
	for _, lane := range []string{"staging", "production"} {
		job, ok := caller.Jobs[lane]
		if !ok {
			t.Errorf("caller workflow is missing %s", lane)
			continue
		}
		if diff := diffStringMaps(wantPermissions, job.Permissions); diff != "" {
			t.Errorf("%s caller permissions (-want +got):\n%s", lane, diff)
		}
	}
}

func diffStringMaps(want, got map[string]string) string {
	var differences []string
	for key, wantValue := range want {
		if gotValue := got[key]; gotValue != wantValue {
			differences = append(differences, "- "+key+": "+wantValue, "+ "+key+": "+gotValue)
		}
	}
	for key, gotValue := range got {
		if _, ok := want[key]; !ok {
			differences = append(differences, "+ "+key+": "+gotValue)
		}
	}
	return strings.Join(differences, "\n")
}
