# Staging release intent

This source revision is the deliberate first release trigger for the protected
Fleet canary lane. The release remains gated by
`NORN_FLEET_CANARY_STAGING_RELEASE_ENABLED`; merging this note while that
variable is `false` has no runtime effect.

This revision retries that release with the protected default-branch check
suite binding. It changes no canary workload behavior.

The next protected push retries after the staging workflow creates the SPDX
output directory before SBOM generation. The canary workload remains unchanged.

Before enabling and merging, operators must verify all of the following:

- the protected app runtime advertises Fleet GitHub OIDC exchange, staging
  release admission, deployment, and operation read capabilities through the
  runner-local TLS route;
- the reviewed one-use runner gate admits only the exact protected `master`
  run and `staging-runner-private` effect job;
- the hosted `Fleet canary` check succeeds for this exact source revision; and
- the runner-local API CA path and SHA-256 match the reviewed staging
  environment variables.
- the staging control has a configured `norn-fleet-canary` Fleet app target
  matching the catalog's `nyc3` region, `global` Nomad region, and `nyc3`
  datacenter; and
- the control's release OIDC audience, reusable workflow pin, and public
  keyless attestation policy match the protected staging workflow.

The first target was configured and read back at revision 2117 on 2026-11-01.
The previous release attempt reached the control but was refused before
artifact verification because that target did not yet exist.

The next attempt reached artifact verification and exposed missing Docker
Buildx, an inaccessible service HOME, a Cosign version mismatch, and Go 1.27.0
vulnerability findings. The staging controls now have Buildx and a writable
HOME; the verifier uses the workflow's Cosign v2.6.5, the canary build is pinned
to Go 1.27.3, and the API parses verified attestation JSON from stdout. The
Go 1.27.3 image passed the control's HIGH/CRITICAL Trivy policy before this
retry. This protected push tests those corrections through the full lane.

A successful release operation proves durable canary admission and deployment;
this Fleet staging lane does not issue a production promotion qualification.
Public route health, two distinct healthy replicas, and later 2-to-3-to-2 scale
fitness remain separate live evidence.
