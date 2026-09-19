# GitHub app-release pipeline

Use the reusable [Norn app release workflow](../../../.github/workflows/norn-app-release.yml)
from an application repository. It deliberately separates environments:

```text
merge to the default branch
  -> wait for caller-supplied successful checks on that exact SHA
  -> build and publish one GHCR digest
  -> keyless signing plus provenance/SBOM attestations
  -> workload identity exchange, staging preflight, and deploy
  -> signed staging qualification artifact

protected v* tag at the same commit
  -> download the matching qualification artifact
  -> production workload identity exchange and same-digest promotion

protected-default-branch recovery dispatch
  -> requalify an existing staging deployment, or restore an admitted production deployment
```

The production job never builds, tags, or substitutes an image. It only sends
the verbatim signed receipt and its matching source SHA/digest to the production
control plane. The control plane validates the receipt's staging identity,
signature, expiry, app, SHA, and digest again before queuing normal durable
deployment work.

The reusable workflow itself fails closed unless staging was invoked by a
protected push to the caller repository's default branch, or production was
invoked by a protected `v*` tag push. Its recovery lane requires an explicit
`workflow_dispatch` from the protected default branch. It does not trust the
caller's `lane` input or documentation alone. Production also accepts an
artifact only when its ZIP contains exactly the expected `qualification.json`
entry.

This v1 workflow supports public caller repositories only. Its validation job
requires the caller GitHub context to report `repository.visibility=public`
before it checks out, builds, signs, or exchanges workload identity. The Norn
API independently verifies the signed GitHub OIDC `repository_visibility`
claim. Private and internal repositories use GitHub private Sigstore
attestations and are deliberately unsupported until Norn has a separate
read-only Release Attestation GitHub App verifier. Do not add a GitHub token or
copy an attestation to bypass that boundary.

For staging, `required_checks` is a caller-supplied, newline-delimited list of
exact check-run identities in the strict format `Exact Check Name<TAB>GitHub App
numeric ID`. The workflow polls GitHub for the exact 40-character push SHA
before publishing: every name/App pair must have exactly one run and finish
`success`. This prevents another GitHub App from spoofing a trusted check name.
Missing, duplicate, pending, neutral, skipped, or failed checks all fail closed.
Configure the same checks in branch protection; this is defence in depth, not a
substitute for protected-branch policy.

## Application workflow

Place the following in the application repository as
`.github/workflows/release.yml`. Replace the reusable workflow ref with a
reviewed, immutable full Norn commit SHA (not a branch name), along with the
app and image names. Use the repository's actual protected default branch in
the `branches` filter.

```yaml
name: Release through Norn

on:
  push:
    branches: [main]
    tags: ['v*']
  workflow_dispatch:
    inputs:
      lane:
        description: Choose requalify for fresh staging evidence or rollback for an admitted production deployment.
        required: true
        type: choice
        options: [requalify, rollback]
      source_sha:
        description: Full SHA of the already-staged commit.
        required: true
        type: string
      deployment_id:
        description: UUID of the successful immutable staging deployment.
        required: true
        type: string
      rollback_source_sha:
        description: Full SHA of the admitted production deployment to restore.
        required: false
        type: string
      rollback_deployment_id:
        description: UUID of the admitted production deployment to restore.
        required: false
        type: string
      rollback_artifact:
        description: Exact digest-pinned artifact from that deployment.
        required: false
        type: string
      rollback_confirmation:
        description: Type ROLLBACK to authorize a production rollback.
        required: false
        type: string

jobs:
  staging:
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: staging
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      required_checks: |
        api	12345
        web	12345
        integration / smoke	67890
    secrets: inherit

  production:
    if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: production
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
    secrets: inherit

  requalify:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'requalify'
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: requalify
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      source_sha: ${{ inputs.source_sha }}
      deployment_id: ${{ inputs.deployment_id }}
    secrets: inherit

  rollback:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'rollback'
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: rollback
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      rollback_source_sha: ${{ inputs.rollback_source_sha }}
      rollback_deployment_id: ${{ inputs.rollback_deployment_id }}
      rollback_artifact: ${{ inputs.rollback_artifact }}
      rollback_confirmation: ${{ inputs.rollback_confirmation }}
    secrets: inherit
```

If the repository's default branch might change, replace `main` with the
protected branch explicitly; a release lane should never infer a deploy branch
from an untrusted pull-request event. The workflow resolves annotated tags to
their underlying commit, so a tag has the same full SHA as its staging receipt.

## GitHub configuration

Create GitHub Environments named `staging` and `production`. Configure this
environment-scoped secret and variable:

| Setting | Meaning |
| --- | --- |
| `NORN_API_URL` | HTTPS base URL of that environment's Norn control API, without credentials. |
| `NORN_GITHUB_ACTIONS_OIDC_AUDIENCE` | Environment variable matching the audience configured on Norn; it is not a credential. |

On each staging and production Norn control plane, set
`NORN_GITHUB_ACTIONS_DEFAULT_BRANCH` to the caller repository's protected
default branch name, such as `main` or `trunk`. This is not a ref value:
`refs/heads/main` is rejected. Norn derives the branch ref and enforces it for
staging, qualification, and rollback; the promotion lane remains protected
`refs/tags/v*`. Keep this setting synchronized with the repository default
branch before changing that branch.

Protect the `production` environment with required reviewers and restrict tag
creation and release workflow changes to trusted maintainers. Keep the staging
environment separate, even when it has production-like admission enabled.
Do not add Norn qualification or artifact-signing private keys to GitHub:
qualification keys remain on the control planes and images are signed keylessly
using GitHub OIDC. Norn verifies issuer/JWKS, expiry, repository/owner IDs,
environment, ref, and the full-SHA-pinned reusable workflow identity, then
exchanges the GitHub token at the last responsible moment for a narrowly scoped
short-lived Norn token.

The staging job publishes to GHCR with the job's short-lived `GITHUB_TOKEN`,
then keylessly signs the exact `@sha256:` digest with the
`norn.git.sha=<full SHA>` Cosign annotation. It generates an SPDX SBOM and
publishes GitHub/Sigstore-backed OCI provenance and SBOM attestations for the
same digest. The package must permit the repository workflow to write it, and
the GitHub plan must support artifact attestations. It stores the signed receipt
in a seven-day artifact named for the app and full source SHA. The receipt
contains no private key, and the production API cryptographically verifies its
full signed v2 envelope, source, and digest before use.

Norn's staging receipt expires after seven days. Create and approve the
production tag while its receipt artifact is retained and unexpired. If it has
expired, a promotion needs recovery evidence, or a prior attempt could not use
its receipt, dispatch the `requalify` lane from the protected default branch.
Give it the original full SHA and successful staging deployment UUID. Norn will
verify that persisted deployment and issue a new receipt for the same recorded
digest; the workflow does not check out code, build, sign, preflight, or deploy
anything. The fresh artifact is retained for the same seven-day window as the
new receipt. Do not rebuild a new image for the production tag.

For a production incident, dispatch `rollback` from the protected default
branch and enter the full source SHA, deployment UUID, exact `@sha256:`
artifact, and literal `ROLLBACK`. Production environment approval still applies.
Norn verifies the recorded admitted deployment, queues its durable exact
rollback, and the workflow waits while recording in-progress and terminal
GitHub deployment status. It never checks out, builds, signs, tags, or
substitutes an image. Use a normal protected release tag with the original
qualification for an intentional forward re-promotion, not this rollback lane.

## Control-plane prerequisites

The staging Norn instance must advertise `environment.id=staging`, have
`NORN_QUALIFICATION_SIGNING_KEY` configured, and already know an InfraSpec for
the same `app_id` and source repository. The production instance must advertise
`environment.id=production`, run with `NORN_PROFILE=production`, and trust the
staging signing key through `NORN_TRUSTED_QUALIFICATION_SIGNING_KEYS`.

Both control planes must be able to resolve the full commit SHA in the app
repository and pull the digest-pinned GHCR artifact. Configure server-side
artifact signature and vulnerability admission according to the production
readiness policy; the workflow's provenance/SBOM are additive evidence, not a
replacement for control-plane admission.

The `image_repository` must be authorized by the app's InfraSpec and control
plane: it must match the repository portion of a pinned `build.image`, or
`NORN_REGISTRY_URL/<app_id>` when the build spec contains only a Dockerfile.
This prevents a digest signed by a shared publisher key for another app from
being substituted into the release.

Configure each control plane's GitHub Actions workload-identity policy with
the immutable reusable-workflow identity
`antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-sha>`,
the caller repository and owner IDs, environment, and permitted ref patterns. Staging
accepts only the protected default branch; production accepts only protected
`v*` tags; recovery accepts only protected-default-branch dispatch. The API is
the authorization point—workflow conditions are not an authorization substitute.
