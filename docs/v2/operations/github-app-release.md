# GitHub app-release pipeline

Use the reusable [Norn app release workflow](../../../.github/workflows/norn-app-release.yml)
from an application repository. It deliberately separates environments:

```text
merge to the default branch
  -> wait for caller-supplied successful checks on that exact SHA
  -> build and publish one GHCR digest
  -> server-selected public, Norn-private, or Enterprise-private attestations
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

The workflow treats GitHub's signed repository visibility as evidence, not as
the backend selector. A staging-only `release:attest` exchange returns the
control plane's configured mode. Public repositories use `github-public`.
Private and internal repositories normally use `norn-signed-private`; an
Enterprise Cloud installation may instead use the optional `github-private`
adapter. Callers cannot choose or downgrade the mode. Unknown or incompatible
combinations fail before build.

For `norn-signed-private`, the job generates SPDX JSON and asks staging to sign
server-canonical provenance plus the SPDX statement. The signer key stays in
Norn or behind its KMS helper. The returned candidate carries both DSSE
envelopes and is reused verbatim for preflight, deploy, qualification,
production promotion, and rollback. For `github-private`, Norn instead needs a
separate read-only Release Attestation GitHub App verifier.

The workflow never submits a URL for Norn to fetch. GitHub provenance/SBOM URLs
are non-authoritative display metadata only. The private verifier fetches from
GitHub by the candidate's exact bound repository, numeric repository and owner
IDs, and OCI subject digest using its own short-lived installation token.
Missing evidence, release binding, signing/trust configuration, or optional
Enterprise adapter entitlement fails closed. Do not add an App private key,
installation token, Norn signing key, or KMS credential to an application
repository or GitHub Environment.

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

If the repository containing `norn-app-release.yml` is public, private caller
repositories can invoke the pinned reusable workflow without an extra sharing
setting. If that Norn repository is private under a personal account, open its
**Settings → Actions → General → Access** and select **Accessible from
repositories owned by the USERNAME user**. For an organization-owned private
Norn repository, enable the equivalent access for repositories in that
organization. This is a deployment prerequisite: without it, GitHub rejects
the caller before Norn can perform OIDC or release admission.

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
    uses: <github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: staging
      app_id: orders-api
      image_repository: ghcr.io/<github-owner>/orders-api
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
    uses: <github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: production
      app_id: orders-api
      image_repository: ghcr.io/<github-owner>/orders-api

  requalify:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'requalify'
    permissions:
      id-token: write
    uses: <github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: requalify
      app_id: orders-api
      image_repository: ghcr.io/<github-owner>/orders-api
      source_sha: ${{ inputs.source_sha }}
      deployment_id: ${{ inputs.deployment_id }}

  rollback:
    if: github.event_name == 'workflow_dispatch' && inputs.lane == 'rollback'
    permissions:
      deployments: write
      id-token: write
    uses: <github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-commit-sha>
    with:
      lane: rollback
      app_id: orders-api
      image_repository: ghcr.io/<github-owner>/orders-api
      rollback_source_sha: ${{ inputs.rollback_source_sha }}
      rollback_deployment_id: ${{ inputs.rollback_deployment_id }}
      rollback_artifact: ${{ inputs.rollback_artifact }}
      rollback_confirmation: ${{ inputs.rollback_confirmation }}
```

For the direct Fleet MySQL pilot, call the same staging reusable workflow with
the explicit `pilot-go` build contract. `APP_VERSION` is injected by the
workflow from the exact admitted source SHA; never pass it from caller input.
Both base images must be lowercase digest-pinned OCI references, and the
pilot's Dockerfile consumes them as `GO_IMAGE` and `RUNTIME_IMAGE`:

```yaml
      app_id: hello-norn-mysql
      dockerfile: v2/infra/fleet-pilot/hello-norn-mysql/Dockerfile
      build_context: v2/infra/fleet-pilot/hello-norn-mysql
      build_contract: pilot-go
      go_image: registry.example/go@sha256:<64-lowercase-hex>
      runtime_image: registry.example/static@sha256:<64-lowercase-hex>
      required_checks: |
        Fleet pilot workload	<GITHUB_ACTIONS_APP_NUMERIC_ID>
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

When the control API is private on Tailscale, set `private_network: true` and
`tailscale_target` to the exact pathless MagicDNS hostname in every caller lane.
The reusable workflow then requires these environment-scoped secrets; it never
inherits them from the caller:

| Secret | Meaning |
| --- | --- |
| `NORN_TAILSCALE_OAUTH_CLIENT_ID` | Environment-specific OAuth client ID with only the Tailscale `auth_keys` scope and permission to issue the matching release-CI tag. |
| `NORN_TAILSCALE_OAUTH_SECRET` | Matching OAuth secret. Rotate it independently in staging and production. |

```yaml
    with:
      private_network: true
      tailscale_target: norn-staging.example-tailnet.ts.net
```

The workflow pins Tailscale's action, client version, and Linux-amd64 archive
digest; it creates an ephemeral preapproved node and waits for the exact
target. The node has the deterministic run-bound prefix
`norn-ci-<lane>-<run-id>-<attempt>`. Tailscale removes an ephemeral node after
the runner disappears, but a disposable pilot must also enumerate and delete
those exact run-scoped devices before its final-zero receipt. The input hostname must exactly equal the hostname
in `NORN_API_URL`; credentials, alternate ports, paths, query strings, and
fragments are rejected. The `staging` and `requalify` lanes use
`tag:norn-release-staging-ci`; `production` and `rollback` use
`tag:norn-release-production-ci`.

Make those tags ownerable only by the respective OAuth clients. Grant each tag
TCP 443 only to the corresponding Norn control-plane tag, and add policy tests
that deny SSH, Nomad, Consul, database, observability, peer-CI, and cross-lane
access. For example, the disposable pilot staging grant is intentionally only:

```json
{
  "src": ["tag:norn-release-staging-ci"],
  "dst": ["tag:norn-pilot-control"],
  "ip": ["tcp:443"]
}
```

Do not grant this CI tag to the generic pilot-node tag if control nodes can
carry a dedicated tag. The build still runs on a fresh GitHub-hosted runner;
the overlay is a network path, not a provider credential or a self-hosted
execution trust boundary. Protected refs, environment policy, GitHub OIDC,
Norn's app/repository binding, and short-lived Norn scopes remain mandatory.

Do not use `secrets: inherit` in a caller. The reusable workflow receives no
caller secrets: after its job declares `staging` or `production`, GitHub makes
that Environment's `NORN_API_URL` and `NORN_GITHUB_ACTIONS_OIDC_AUDIENCE`
variables available through `vars` and, when enabled, makes the two narrowly
scoped Tailscale OAuth values available through `secrets`. Where the GitHub plan supports environment
reviewers, treat them as defense in depth. A personal private repository may
not have that reviewer feature; Norn's production OIDC/ref policy and signed
promotion gate remain authoritative. No GitHub App credential is supplied to
the workflow.

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
NORN_GITHUB_ACTIONS_RELEASE_BINDINGS=orders-api=<github-owner>/orders-api@123456789@987654321
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

Protect the `production` environment with required reviewers when available,
and always restrict tag creation and release workflow changes to trusted
maintainers. Keep the staging
environment separate, even when it has production-like admission enabled.
Do not add Norn qualification or artifact-signing private keys to GitHub:
qualification and private-evidence keys remain on the control planes or behind
their KMS helper. Norn verifies issuer/JWKS, expiry, repository/owner IDs,
environment, ref, and the full-SHA-pinned reusable workflow identity, then
exchanges the GitHub token at the last responsible moment for a narrowly scoped
short-lived Norn token.

The staging job publishes to GHCR with the job's short-lived `GITHUB_TOKEN` and
always generates an SPDX SBOM for the exact digest. In `github-public` and the
optional `github-private` mode it uses the pinned `actions/attest@v4`; public
also adds the Cosign `norn.git.sha=<full SHA>` signature. In
`norn-signed-private`, it skips GitHub artifact attestation and sends the SPDX
document to the staging signer through a short-lived `release:attest` token.
The signed qualification remains a seven-day artifact; it contains public
evidence and signatures, never private key material.

## Ordinary private repositories (`norn-signed-private`)

The GitHub owner may be a personal account or an organization; it is not part
of Norn's environment identity. Use neutral deployment names such as
`norn-staging` and `norn-production`, then bind each app to the actual owner and
numeric GitHub identities:

```sh
NORN_RELEASE_ADMISSION_MODE=attested
NORN_RELEASE_ATTESTATION_TRUST_MODE=norn-signed-private
NORN_RELEASE_ATTESTATION_ISSUER=https://token.actions.githubusercontent.com
NORN_RELEASE_ATTESTATION_ALLOWED_REPOSITORIES=<github-owner>/orders-api
NORN_RELEASE_ATTESTATION_ALLOWED_WORKFLOW_REFS=<github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-sha>
NORN_RELEASE_REQUIRE_SBOM=true
NORN_RELEASE_REGISTRY_NODE_PULL_READY=true
NORN_RELEASE_ATTESTATION_REGISTRY_AUTH_FILE=/absolute/owner-only/docker-config.json
NORN_RELEASE_PRIVATE_TRUSTED_SIGNING_KEYS=<base64-public-key>
```

On staging only, configure one signer:

```sh
# Usable pilot backend. The PKCS#8 Ed25519 file must be owned by the service
# user, mode 0600, and must never be copied to Actions or production.
NORN_RELEASE_PRIVATE_SIGNING_BACKEND=local
NORN_RELEASE_PRIVATE_SIGNING_KEY_FILE=/absolute/owner-only/norn-private-ed25519.pem

# Or delegate to an operator-supplied KMS/HSM bridge.
NORN_RELEASE_PRIVATE_SIGNING_BACKEND=kms-helper
NORN_RELEASE_PRIVATE_KMS_HELPER=/absolute/operator-owned/norn-kms-sign
NORN_RELEASE_PRIVATE_KMS_KEY_ID=sha256:<configured-public-key-id>
```

The KMS helper receives raw DSSE PAE bytes on standard input and must return one
bounded JSON object with `keyId` and an unpadded-base64 `sig`. It is an
implemented provider-neutral process boundary; deploying the provider-specific
helper and its workload identity remains operator work. The helper receives an
empty process environment, runs from `/`, and has a 30-second signing deadline;
use its service sandbox, metadata identity, or an explicitly provisioned
owner-only credential source instead of ambient Norn environment variables.
Production must not be configured with the
signing backend, private key, helper, or KMS key ID. It receives only
`NORN_RELEASE_PRIVATE_TRUSTED_SIGNING_KEYS` and independently verifies the
portable evidence plus staging qualification.

This path is required only for managed staging/production workloads. A Mac Mini
or other local development host should remain `NORN_ENVIRONMENT=development`;
it needs neither Fleet nor release-signing configuration. DigitalOcean support
does not alter signing: the protected Fleet runner owns DigitalOcean and state
credentials, while the workload release uses the same qualification/promotion
contract on any provider.

## Optional GitHub Enterprise Cloud adapter (`github-private`)

Organizations with GitHub Enterprise Cloud may choose GitHub's private
attestation service instead. Configure `github-private` plus a separate
repository-scoped Release Attestation GitHub App with only `Attestations: read`.
Its installation/private key stays on the control planes and is never reused as
the Fleet App, GHCR pull credential, or CI credential. Private/internal GitHub
artifact attestations, a missing entitlement, App installation, bundle, or
verifier configuration all fail closed. This adapter changes only evidence
verification; staging qualification, production promotion, and rollback remain
identical.

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
artifact, and literal `ROLLBACK`. Production environment required-reviewer
approval applies when the GitHub plan and repository visibility support it;
Norn's promotion/rollback gate remains authoritative in every case.
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

The managed staging Norn instance must advertise `environment.id=staging`, run
with `NORN_PROFILE=production`, have `NORN_QUALIFICATION_SIGNING_KEY`
configured, and already know an InfraSpec for the same `app_id` and source
repository. Treat the production profile as a deployment prerequisite for a
staging signer because its qualification is production authority. The local
Mac development instance remains `NORN_ENVIRONMENT=development` and needs none
of these keys. The production instance must advertise
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

Before the first private-repository release, confirm that the pinned Norn
reusable-workflow repository is either public or explicitly shared with the
calling personal account or organization through **Actions → General →
Access**.

Configure each control plane's GitHub Actions workload-identity policy with the
authoritative exact app-to-repository binding above, the immutable reusable-workflow identity
`<github-owner>/norn/.github/workflows/norn-app-release.yml@<full-norn-sha>`,
environment, and permitted ref patterns. Staging
accepts only the protected default branch; production accepts only protected
`v*` tags; recovery accepts only protected-default-branch dispatch. The API is
the authorization point—workflow conditions are not an authorization substitute.
