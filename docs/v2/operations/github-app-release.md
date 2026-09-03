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

The workflow derives its attestation mode from GitHub's trusted repository
visibility context and its signed OIDC claim: public repositories use
`github-public`; private and internal repositories use `github-private`; any
other value fails before checkout. Callers cannot choose the mode. The public
mode adds a keyless Cosign signature. The private mode deliberately omits that
public-Sigstore signature and relies on GitHub's private provenance and SBOM
attestations. Norn must have its separate read-only Release Attestation GitHub
App verifier configured before a private or internal release can succeed.

The workflow never submits a URL for Norn to fetch. GitHub provenance/SBOM URLs
are non-authoritative display metadata only. The private verifier fetches from
GitHub by the candidate's exact bound repository, numeric repository and owner
IDs, and OCI subject digest using its own short-lived installation token.
Missing entitlement, App installation, attestation, release binding, or verifier
configuration fails closed. Do not add an App private key, installation token,
or a copied bundle to an application repository, GitHub Environment, or Norn CI
request.

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
    # Reusable workflows can only reduce caller permissions. Declare every
    # permission the called staging jobs need so this works with an
    # organization default of read-only.
    permissions:
      checks: read
      contents: read
      packages: write
      id-token: write
      attestations: write
      artifact-metadata: write
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: staging
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      required_checks: |
        api	12345
        web	12345
        integration / smoke	67890

  production:
    if: github.event_name == 'push' && startsWith(github.ref, 'refs/tags/v')
    permissions:
      actions: read
      contents: read
      id-token: write
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: production
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api

  requalify:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'requalify'
    permissions:
      id-token: write
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: requalify
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      source_sha: ${{ inputs.source_sha }}
      deployment_id: ${{ inputs.deployment_id }}

  rollback:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'rollback'
    permissions:
      deployments: write
      id-token: write
    uses: antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: rollback
      app_id: orders-api
      image_repository: ghcr.io/acme/orders-api
      rollback_source_sha: ${{ inputs.rollback_source_sha }}
      rollback_deployment_id: ${{ inputs.rollback_deployment_id }}
      rollback_artifact: ${{ inputs.rollback_artifact }}
      rollback_confirmation: ${{ inputs.rollback_confirmation }}
```

If the repository's default branch might change, replace `main` with the
protected branch explicitly; a release lane should never infer a deploy branch
from an untrusted pull-request event. The workflow resolves annotated tags to
their underlying commit, so a tag has the same full SHA as its staging receipt.

Keep these caller-job permissions explicit. A reusable workflow can narrow its
caller's `GITHUB_TOKEN` permissions but cannot elevate a read-only organization
or repository default. Do not replace the blocks with `write-all`; each lane
receives only the GitHub capability it actually uses.

## GitHub configuration

Create GitHub Environments named `staging` and `production`. Configure these
environment-scoped variables:

| Setting | Meaning |
| --- | --- |
| `NORN_API_URL` | HTTPS base URL of that environment's Norn control API, without credentials. |
| `NORN_GITHUB_ACTIONS_OIDC_AUDIENCE` | Environment variable matching the audience configured on Norn; it is not a credential. |

Do not use `secrets: inherit` in a caller. The reusable workflow receives no
caller secrets: after its job declares `staging` or `production`, GitHub makes
that Environment's `NORN_API_URL` and `NORN_GITHUB_ACTIONS_OIDC_AUDIENCE`
variables available through `vars`. The matching Environment also supplies its
reviewer gate. No GitHub App credential is supplied to the workflow.

On each staging and production Norn control plane, set
`NORN_GITHUB_ACTIONS_DEFAULT_BRANCH` to the caller repository's protected
default branch name, such as `main` or `trunk`. This is not a ref value:
`refs/heads/main` is rejected. Norn derives the branch ref and enforces it for
staging, qualification, and rollback; the promotion lane remains protected
`refs/tags/v*`. Keep this setting synchronized with the repository default
branch before changing that branch.

Each Norn control plane must also authorize every release app through the
authoritative `NORN_GITHUB_ACTIONS_RELEASE_BINDINGS` setting. It is a
comma-separated set of exact, immutable tuples in this format:

```sh
# app_id=owner/repository@GitHub-repository-ID@GitHub-owner-ID
NORN_GITHUB_ACTIONS_RELEASE_BINDINGS=orders-api=acme/orders-api@123456789@987654321
```

For multiple applications, add one tuple per app, separated by commas. The
`app_id` must exactly equal the workflow's `app_id`; the repository name and
both numeric IDs must exactly match GitHub's signed OIDC claims. This is an
app-to-repository mapping, not two independent allowlists, so a workflow from
one allowed repository cannot request another allowed app.

`NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES` and
`NORN_GITHUB_ACTIONS_ALLOWED_APPS` are legacy parsed settings only. They are
non-authorizing for every release lane and must not be used as a release
configuration or migration fallback. Fleet GitHub authorization remains
separate and uses its own `NORN_GITHUB_ACTIONS_FLEET_*` policy.

Staging and production refuse startup when this binding setting, the OIDC
audience, immutable reusable-workflow identity, lane ref/event/environment
policy, or protected default branch configuration is missing. The authenticated
capabilities response advertises `release-app-repository-bindings-v1` without
disclosing configured bindings or any credentials.

Protect the `production` environment with required reviewers and restrict tag
creation and release workflow changes to trusted maintainers. Keep the staging
environment separate, even when it has production-like admission enabled.
Do not add Norn qualification or artifact-signing private keys to GitHub:
qualification keys remain on the control planes and images are signed keylessly
using GitHub OIDC. Norn verifies issuer/JWKS, expiry, repository/owner IDs,
environment, ref, and the full-SHA-pinned reusable workflow identity, then
exchanges the GitHub token at the last responsible moment for a narrowly scoped
short-lived Norn token.

The staging job publishes to GHCR with the job's short-lived `GITHUB_TOKEN`.
It keylessly signs the exact `@sha256:` digest with the
`norn.git.sha=<full SHA>` Cosign annotation only for a public repository. It
always generates an SPDX SBOM and GitHub provenance/SBOM attestations for the
same digest using the pinned `actions/attest@v4` action: its provenance mode is
selected by omitting an SBOM/custom predicate, and its SBOM mode is selected by
the generated SPDX JSON. The job needs both `attestations: write` and
`artifact-metadata: write`: the latter creates GitHub's Linked Artifacts record
for the digest and does not replace the former attestation permission. The
package must permit the repository workflow to write it. Private and internal
attestations require GitHub Enterprise Cloud. The signed qualification remains a
seven-day artifact; it contains no private key, and production verifies its v2
envelope, source, digest, and attestation mode before use.

## GitHub Enterprise Cloud private-organization pilot

Use the following isolated thirty-day pilot before enabling this path for other
repositories:

| Item | Pilot value |
| --- | --- |
| Enterprise | `norn-pilot` |
| Organization | `norn-pilot-labs` |
| Private application repository and app ID | `norn-private-pilot` |
| GHCR package | `ghcr.io/norn-pilot-labs/norn-private-pilot` |
| Private Fleet repository | `norn-fleet-pilot` |
| Staging control plane / fleet | `norn-pilot-staging-control` / `norn-pilot-staging` |
| Production control plane / fleet | `norn-pilot-production-control` / `norn-pilot-production` |
| GitHub Environments | `staging`, `production` |
| Release verifier App | `Norn Pilot Release Verifier` |

1. Confirm the `norn-pilot` Enterprise Cloud trial or subscription permits
   private/internal artifact attestations, then create protected default-branch
   and `v*` tag rules for `norn-private-pilot`.
2. Create `Norn Pilot Release Verifier` as a distinct GitHub App. Install it
   only on `norn-private-pilot`, grant only `Attestations: read`, and configure
   its private key solely on `norn-pilot-staging-control` and
   `norn-pilot-production-control`. It is not the Fleet App and is never
   installed on `norn-fleet-pilot`.
3. Configure each control plane with
   `NORN_GITHUB_ACTIONS_RELEASE_BINDINGS=norn-private-pilot=norn-pilot-labs/norn-private-pilot@<repository-id>@<owner-id>`,
   the immutable reusable Norn workflow SHA, the expected environment/ref lane,
   and `github-private` attestation mode. Test that an unknown digest, another
   app, or a repository outside that exact binding is denied.
4. Let the application workflow use its short-lived `GITHUB_TOKEN` only for
   GHCR publish and GitHub attestations. Give each control plane a separate
   read-only GHCR package pull credential or workload identity for
   `ghcr.io/norn-pilot-labs/norn-private-pilot`; it must not be the verifier
   App's token. Likewise, use a distinct read-only source credential for source
   resolution if the control plane needs one. Neither server-side credential is
   exposed to Actions.
5. Keep `norn-pilot-staging` and `norn-pilot-production` separate, run one
   merge-to-staging and one same-digest protected-tag promotion, then rehearse
   requalification and rollback. Confirm the verifier's audit record names the
   exact repository and digest and never logs an installation token.

The pilot expires after 30 days. Before expiry, explicitly convert it to a
funded Enterprise Cloud configuration or tear it down: uninstall the verifier
App, revoke its private keys and package/source pull credentials, remove the
release bindings and environments, and keep Norn's private-attestation
mode fail-closed. Do not silently fall back to public Sigstore, copied bundles,
or static CI credentials.

Control planes cache GitHub's OIDC JWKS only for a bounded lifetime and make
one coalesced refresh when GitHub rotates to an unknown key ID; repeated random
key IDs do not create an outbound refresh storm. If the cache has expired while
GitHub is unavailable, release exchanges fail closed. In production configure
`NORN_COSIGN_PATH` and `NORN_TRIVY_PATH` as absolute paths. Verifier App keys
and private registry-auth files must be owner-only regular files (not
symlinks); Norn rechecks them immediately before every use.

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

This pilot rollback is deliberately **online-only**. Before it mutates the
workload, Norn rechecks GitHub's private-attestation API, the exact GHCR
digest, and vulnerability scanning with its server-side credentials. It is not
an offline or disaster-recovery rollback guarantee: if GitHub, GHCR, the
scanner, or their required control-plane network paths are unavailable, the
rollback fails closed. Maintain a separately rehearsed infrastructure/disaster
recovery plan for an outage that removes those dependencies.

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
being substituted into the release. Norn also canonicalizes the InfraSpec's
GitHub HTTPS/SSH source URL and requires it to equal the signed candidate
repository; the reusable workflow's `image_repository` and `app_id` inputs do
not authorize either relationship.

Configure each control plane's GitHub Actions workload-identity policy with the
authoritative exact app-to-repository binding above, the immutable reusable-workflow identity
`antiartificial/norn/.github/workflows/norn-app-release.yml@<full-norn-sha>`,
environment, and permitted ref patterns. Staging
accepts only the protected default branch; production accepts only protected
`v*` tags; recovery accepts only protected-default-branch dispatch. The API is
the authorization point—workflow conditions are not an authorization substitute.
