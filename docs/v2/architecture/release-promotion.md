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
admission. Staging may deliberately use `NORN_PROFILE=production`; in that
case `NORN_ENVIRONMENT=staging` must be explicitly configured. Existing
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
    "workflowRef": "owner/repository/.github/workflows/release.yml@<full SHA>",
    "workflowSha": "<full SHA>",
    "ref": "refs/heads/<configured-default-branch>",
    "attestation": {
      "mode": "github-public | github-private",
      "issuer": "https://token.actions.githubusercontent.com",
      "subjectDigest": "<same digest-pinned artifact>",
      "materialSHA": "<same full source SHA>"
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
GitHub OIDC to obtain short-lived Norn mutation tokens and uses keyless OCI
provenance/SBOM attestations; no static Norn token or CI-held Cosign private key
is part of this trust path.

The reusable workflow derives `attestation.mode` from GitHub's repository
visibility context, not from a caller input: `public` becomes `github-public`;
`private` and `internal` become `github-private`; an unknown visibility fails
before checkout. Norn independently verifies the signed GitHub OIDC
`repository_visibility` claim and must require it to agree with the candidate.

`github-public` uses the public Sigstore trust path and an additional keyless
Cosign signature. `github-private` is an Enterprise Cloud-only path: GitHub
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

The reusable [GitHub app-release pipeline](../operations/github-app-release.md)
implements this handoff for a merge to staging and a protected tag to
production.
