# Staging and production release promotion

Norn uses separate control planes and fleets for staging and production. The
same `norn-fleet` repository may define both roots, but each environment has
independent Norn state, credentials, runner identity, database, and runtime
substrate. A region is a placement concern inside one environment; it is not a
staging/production boundary.

`NORN_ENVIRONMENT` identifies the release lane and is one of `development`,
`staging`, or `production`. It is advertised from `GET /api/v1/capabilities`
as `environment.id`, together with the independent `environment.profile`.
`NORN_PROFILE` continues to control local production hardening and substrate
admission. The platform permits staging with either profile for migration, but
a managed staging signer should use `NORN_PROFILE=production` because its
qualification is production authority; in that case
`NORN_ENVIRONMENT=staging` must be explicitly configured. Existing
production-profile instances with an omitted or `development` environment fail
closed at startup: set `NORN_ENVIRONMENT=staging` or `production` as part of
the migration. A production lane is refused unless both
`NORN_ENVIRONMENT=production` and `NORN_PROFILE=production` are set. A
release-capable server advertises
`release-app-repository-bindings-v1` in its capabilities. This advertises the
exact-binding authorization model, never the configured app names, repository
names, numeric IDs, or credentials.

## Build once, deploy the digest

CI builds, scans, signs, and publishes a single OCI digest for the full source
SHA. A merge to the configured protected default branch queues staging through:

```text
POST /api/v1/apps/{app}/releases/preflight
POST /api/v1/apps/{app}/releases/deployments
Idempotency-Key: stable retry key

{
  "sourceSha": "<40 lowercase hex>",
  "artifact": "registry.example/app@sha256:<64 hex>",
  "candidate": {
    "provider": "github-actions",
    "repository": "owner/repository",
    "repositoryId": "<GitHub repository ID>",
    "ownerId": "<GitHub owner ID>",
    "runId": "<GitHub run ID>",
    "runAttempt": "<attempt>",
    "workflowRef": "owner/repository/.github/workflows/release.yml@refs/heads/<configured-default-branch>",
    "workflowSha": "<full SHA containing the caller workflow>",
    "signerWorkflowRef": "owner/norn/.github/workflows/norn-app-release.yml@<full Norn SHA>",
    "signerWorkflowSha": "<same full Norn SHA>",
    "ref": "refs/heads/<configured-default-branch>",
    "attestation": {
      "mode": "github-public | norn-signed-private | github-private",
      "issuer": "https://token.actions.githubusercontent.com",
      "subjectDigest": "<same digest-pinned artifact>",
      "materialSha": "<same full source SHA>"
    }
  }
}
```

Both endpoints use the existing durable `app.preflight` or `app.deploy`
operation, saga, deployment steps, locks, rollback behavior, and operation
receipt. They return `202 Accepted` and the normal operation ID, saga ID, and
deployment ID. The source SHA, artifact, and local environment are persisted
with that durable intent. When `artifact` is supplied, the regular pipeline
verifies/adopts that digest and skips rebuilding it; it never replaces it with
a mutable tag. A source checkout must resolve to the exact requested SHA.
These direct release endpoints are staging-only. A production control plane
accepts this release channel only through the signed promotion endpoint. The
control plane authorizes this candidate against the GitHub OIDC identity rather
than accepting caller-controlled repository or workflow metadata.

For staging and production control planes, set
`NORN_GITHUB_ACTIONS_DEFAULT_BRANCH` to the caller repository's protected
default **branch name** (for example `main` or `trunk`), not a full Git ref.
Norn derives `refs/heads/<branch>` itself and uses it for staging, initial
qualification, requalification, and rollback authorization. A promotion keeps
the separate protected `refs/tags/v*` lane.

```sh
# Control-plane configuration for a repository whose protected default branch is trunk.
NORN_GITHUB_ACTIONS_DEFAULT_BRANCH=trunk
# Each tuple binds exactly one Norn app to one immutable GitHub repository identity.
NORN_GITHUB_ACTIONS_RELEASE_BINDINGS=orders-api=acme/orders-api@123456789@987654321
```

`NORN_GITHUB_ACTIONS_RELEASE_BINDINGS` is the sole repository authorization
setting for release OIDC. Each comma-separated tuple is
`app_id=owner/repository@repositoryID@ownerID`; Norn matches every component to
the signed GitHub identity and to the requested URL app. This prevents the
cross-product failure where a workflow from one otherwise allowed repository
could request deployment of another otherwise allowed app. The legacy
`NORN_GITHUB_ACTIONS_ALLOWED_REPOSITORIES` and
`NORN_GITHUB_ACTIONS_ALLOWED_APPS` settings remain parsed only for compatibility
and are non-authorizing for all release lanes. Fleet uses a separate policy.

The legacy `/api/apps/{app}/preflight` route remains available for read-only
diagnostics. In a production environment, legacy app deploy, deploy-group, and
webhook deploy/replay paths are refused (push webhooks are recorded as ignored)
so a moving branch cannot bypass signed promotion.

## Qualification and promotion trust boundary

After a successful staging release deployment, staging creates a portable
receipt:

```text
POST /api/v1/apps/{app}/qualifications
Idempotency-Key: stable retry key

{ "deploymentId": "<UUID>" }
```

Staging issues this only when the stored deployment belongs to that app, is
terminally deployed, records `environment=staging`, carries a full source SHA,
and carries a digest-pinned artifact. The v2 receipt binds app, staging
environment, deployment ID, source SHA, artifact, issue/expiry times, key ID,
and the GitHub Actions candidate identity (repository, run, workflow, ref,
provenance/SBOM issuer, subject digest, and material SHA). It includes a DSSE
envelope whose canonical receipt payload has exactly one Ed25519 signature. It
is a terminal `release.qualification` operation and can be listed at
`GET /api/v1/apps/{app}/qualifications`.

The staging control plane signs with a purpose-specific Ed25519 qualification
key. Production trusts the corresponding public key set, allowing rotation.
Do not put either key in an application repository or CI log. An explicitly
configured staging or production environment fails startup when its required
qualification signing or trust material is absent or invalid; the qualification
key must not be reused for mutation-audit signing.

A protected release tag promotes only the exact signed receipt:

```text
POST /api/v1/apps/{app}/promotions
Idempotency-Key: stable retry key

{
  "sourceSha": "<same full SHA>",
  "artifact": "<same digest>",
  "qualification": { "...": "verbatim staging receipt" }
}
```

Production accepts the request only on a `production` environment control
plane, only when the URL app, source SHA, and artifact exactly match a v2
staging receipt, and only after verifying the configured Ed25519 trust key,
DSSE payload, staging identity, candidate provenance/SBOM binding, expiry, and
bounded clock skew. Missing or incorrect trust material fails closed. It then
queues the standard durable deployment; the qualification is retained in
operation metadata for audit and UI provenance. Each qualification ID can be
consumed by at most one production operation. An idempotent retry resolves to
that operation; an intentional later re-promotion requires staging to issue a
fresh qualification.

Protected CI transports the full signed receipt without assuming the two
independently operated control-plane databases can read each other. It uses
GitHub OIDC to obtain narrowly scoped, short-lived Norn tokens. No static Norn
token or CI-held Cosign/Norn private key is part of this trust path.

The reusable workflow never derives a private-repository trust backend from a
caller input or from visibility alone. It exchanges an OIDC assertion with the
staging control plane using the staging-only `release:attest` scope; Norn
returns its configured `attestationMode`. The signed GitHub
`repository_visibility` claim must agree with that server policy. Public
repositories can use only `github-public`; private/internal repositories can
use `norn-signed-private` or the optional `github-private` adapter. Unknown or
cross-class combinations fail before the image is built.

`github-public` uses the public Sigstore trust path and an additional keyless
Cosign signature.

`norn-signed-private` is the default path for ordinary private repositories.
After the immutable image and SPDX document exist, the protected staging job
uses a fresh GitHub OIDC assertion to obtain only `release:attest`, then sends
the digest, source SHA, and SPDX JSON to staging. Norn independently binds the
numeric repository/owner IDs, app, run, caller workflow, SHA-pinned reusable
workflow, protected ref, digest, and source SHA from the verified token. It
constructs the SLSA provenance statement itself and returns two signed DSSE
envelopes: provenance and SPDX. The response is durably idempotent and becomes
the exact candidate passed to preflight, deployment, qualification, promotion,
and rollback. The request is capped at 3 MiB, decoded SPDX at 2 MiB, and release
evidence requests at 12 MiB (the qualification wraps the signed candidate a
second time).

Workflow operation polling is authorized against the exact app, environment,
scope, subject, and stable GitHub CI identity that queued the operation. It
returns a bounded status projection rather than retransmitting embedded
evidence on every poll. The operator-facing qualification list returns at most
the three most recent unexpired full receipts; immutable promotion artifacts
remain the transfer mechanism for a selected qualification.

The usable pilot signer is an owner-only Ed25519 key file held by the staging
control plane. Production receives only the corresponding public key. A
provider-neutral `kms-helper` backend is also available: an absolute,
operator-owned executable receives DSSE PAE bytes on stdin and returns
`{"keyId":"...","sig":"<unpadded-base64>"}`. The helper owns the actual
KMS/HSM API call; Norn verifies its configured key ID against the trusted public
key before accepting evidence. Norn starts the helper with an empty environment
and `/` as its working directory so database, registry, and control-plane
credentials are not inherited, and terminates signing after 30 seconds.
Provider workload identity must therefore come
from the helper's service sandbox or an explicitly provisioned owner-only
credential source. This is an integration boundary, not a bundled
DigitalOcean/AWS/GCP KMS client.

`github-private` remains an optional Enterprise Cloud-only adapter: GitHub
stores the provenance and SBOM in its private Sigstore instance, so Norn must
use a separately configured, read-only Release Attestation GitHub App to fetch
attestations by the exact bound repository identity and subject digest. It must
verify the returned GitHub bundle and structured provenance/SBOM before
admission. Candidate provenance and SBOM URLs are display metadata only; they
are never treated as a credential, arbitrary bundle location, or verifier input.

The verifier App is separate from the Fleet GitHub App and has only
repository-scoped `Attestations: read` access to selected application
repositories. It has no Fleet, deployment, workflow-write, package-write, or
administration permission. A missing App installation, release binding,
Enterprise entitlement, bundle, or verifier configuration denies the release.

The artifact-signing backend does not change release topology: staging still
qualifies one immutable deployment, production still consumes that portable
qualification, and rollback still re-admits the same digest. It is also
provider-independent. A DigitalOcean Fleet supplies compute/networking and its
runner supplies registry/provider credentials; DigitalOcean never selects or
holds Norn's release-signing key. A local development control plane remains on
`NORN_ENVIRONMENT=development` and does not need Fleet, release OIDC, or this
signing path.

The reusable [GitHub app-release pipeline](../operations/github-app-release.md)
implements this handoff for a merge to staging and a protected tag to
production.
