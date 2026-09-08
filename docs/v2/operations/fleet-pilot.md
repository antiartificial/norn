# Fleet pilot architecture and bootstrap sequence

For the teammate walkthrough and acceptance evidence, see
[Fleet UI acceptance](./fleet-ui-acceptance.md).

This document defines the bounded staging pilot architecture. It is a design
and implementation checklist, not evidence that cloud resources exist and not
authorization to create them. Provider mutation remains in the reviewed
`norn-fleet` workflow.

## Non-negotiable boundaries

- The Mac Mini is an independent `development` environment. It is not a Fleet
  bootstrap controller, a staging replica, or a recovery dependency.
- Staging is a persistent cloud environment. The initial approved topology is
  fixed at three control nodes and two dual-role ingress/workload nodes. It
  exercises the production control, ingress, release, recovery, and networking
  contracts without inventing an `app` pool.
- A disposable rehearsal is a different environment and state root. It never
  reuses, scales to zero, or tears down persistent staging.
- A Fleet plan is durable intent, not a provider mutation. OpenTofu state is
  not a receipt, and an Actions artifact with finite retention is not the
  durable system of record.
- Public application ingress and management access are separate networks.

## What is and is not implemented

The integrated control-plane work now includes an opt-in
`NORN_FLEET_AUTHORITY_ONLY` process mode. It runs database migrations and then
serves an explicit Fleet/auth/operation/audit allowlist without initializing
Nomad, Consul, application and release workers, host repair, exec, snapshots,
webhooks or runtime watchers. It can serve the static management UI without
starting workload services. Startup requires a staging environment,
explicit authentication, verified PostgreSQL TLS, durable mutation audit,
exact Fleet GitHub/OIDC identity, and matching Fleet document metadata. This
capability is present in the integration worktree; it is not yet a released,
qualified, or deployed authority.

The current five-node Fleet root also bootstraps the Norn API onto its control
nodes. Making that in-fleet API the only holder of plan, dispatch, attempt, and
checkpoint records creates a circular dependency during first creation and
loses authority when the target fleet is unavailable or retired.

Therefore the pilot must not be configured or described as hands-off until the
management-only binary is released, its remaining hardening review is closed,
and an external deployment and database are qualified. Merely setting
`NORN_ENVIRONMENT=staging`, enabling development OIDC, or pointing a runner at
the Mini does not close this gap.

## Minimal external Fleet authority

Add one narrowly scoped mode to the existing Norn API rather than adding a new
release environment or overloading the development lane:

```text
NORN_ENVIRONMENT=staging
NORN_PROFILE=development
NORN_FLEET_AUTHORITY_ONLY=true
```

The development profile is an explicit compatibility choice: the production
profile requires live application-substrate admission that a management-only
process intentionally does not own. Authority-only startup supplies its own
fail-closed checks instead of inheriting permissive development defaults.

In authority-only mode Norn now:

1. Requires the explicit staging environment, explicit authentication, a
   TLS-verified external PostgreSQL connection, an audit signing key and
   retention policy, a complete repository-scoped GitHub App configuration,
   and exact GitHub Actions OIDC repository/workflow/ref/environment/intent
   bindings. PITR, off-host backups, and restore drills are separate
   operational qualification gates; a valid connection string cannot prove
   them.
2. Require exactly one configured Fleet document whose metadata environment
   equals `NORN_ENVIRONMENT`. Remove the development-environment wildcard from
   this mode; a staging authority must not operate a production or rehearsal
   root.
3. Serve health, version, capability discovery, Fleet validation/inventory,
   capacity plans, GitHub PR/dispatch, runner attempts, reconciliations,
   operation reads, administrator-only mutation-audit reads, and scoped human
   device enrollment with administrator approval.
4. Keeps attempt expiry on the durable attempt read/update paths, but does not
   initialize Nomad, Consul, app discovery, deploy workers, release
   signing/promotion, host repair, exec, snapshots, webhooks, runtime watchers,
   or local cloudflared management.
5. Advertises the restricted mode and supported routes through capabilities so
   clients do not present application or host actions.

Before release, qualification must also prove that the GitHub API endpoint is
pinned to `api.github.com` in this mode, the GitHub JWKS endpoint is rejected at
startup unless it is the fixed issuer endpoint, and the served OpenAPI document
does not advertise routes absent from the authority router.

The first authority deployment lives outside the staging Fleet root, uses a
different state backend/key and deletion boundary, and remains reachable when
all staging nodes are absent. Its creation and retirement use a separate,
reviewed bootstrap/break-glass infrastructure lane; ordinary staging
apply/recover credentials cannot change it. The authority holds the
repository-scoped GitHub App key and Norn audit material, but never holds the
cloud-provider token, OpenTofu state credentials, node SSH key, or bootstrap
secrets. Those remain bound to protected runner steps.

This pilot may begin with a single staging authority deployment backed by an
external durable database. Production later receives an independently
qualified authority and credentials. Sharing one database, signing key, OIDC
audience, GitHub environment, or bootstrap secret set between staging and
production is out of scope.

## Temporary direct-workload admission

`hello-norn-mysql` remains `deploy: false` until the normal Nomad translator
can express its complete service, secret-file, TLS-routing, migration, and
shutdown contract. Do not flip that bit merely to make a pilot test pass.

The target staging control plane has a deliberately narrow, disabled-by-default
bridge at `POST /api/v1/apps/{id}/external-deployments`. It is only enabled
when all of these server-owned bindings are exact:

```text
NORN_EXTERNAL_FLEET_ADMISSION_APP=hello-norn-mysql
NORN_EXTERNAL_FLEET_ADMISSION_NAMESPACE=<exact Nomad namespace>
NORN_EXTERNAL_FLEET_ADMISSION_MIGRATION_JOB_ID=<exact migration Nomad job ID>
NORN_EXTERNAL_FLEET_ADMISSION_MIGRATION_HCL_SHA256=<released migration-HCL SHA-256>
NORN_EXTERNAL_FLEET_ADMISSION_RUNTIME_JOB_ID=<exact runtime Nomad job ID>
NORN_EXTERNAL_FLEET_ADMISSION_RUNTIME_HCL_SHA256=<released runtime-HCL SHA-256>
NORN_EXTERNAL_FLEET_ADMISSION_BOOTSTRAP_SIGNER_REF=<exact bootstrap workflow path@40-char SHA>
```

Use the same complete bridge binding set on the production control plane to
verify the staging qualification during promotion, but do not expose the
external-admission route there: its protected identity remains staging-only.
The bootstrap signer ref must be absent from
`NORN_RELEASE_ATTESTATION_ALLOWED_WORKFLOW_REFS`; it is a separate, exact
first-image adoption identity, not a normal release signer. Migration and
runtime job IDs and their HCL digests must each be different. A real verifier
is required before nonce issuance as well as receipt admission; at most three
unconsumed nonces may exist for one protected CI run, and expired nonce rows
are removed in bounded batches.

The runner exchanges GitHub OIDC only for `fleet:external-admission`, naming
the route app. The exchange still requires the configured protected
`norn-fleet` repository, SHA-pinned apply/recover workflow, protected staging
environment, protected ref, and `apply` or `recover` intent. A regular Fleet
token, static control token, legacy token, or administrator scope is not a
substitute.

The first POST with `{"action":"issue-nonce"}` produces a short-lived,
one-use Norn nonce bound to that exact CI run. The runner writes it through the
job's restricted runtime path and submits a canonical receipt only after its
Fleet attempt checkpoints. The API persists a redacted normalized proof plus a
nonce hash only; it atomically consumes that hash and writes the terminal
deployment, verified region weights/evaluations, and operation together.

Receipt text is evidence *pointers*, never authority. The server-owned verifier
must independently read the released HCL digest, source/repository, OCI digest,
attestation and SBOM references, Nomad v2 migration/runtime job proof
(`JobID`, `EvalID`, and `JobModifyIndex`), Fleet
plan/run/attempt/checkpoint binding, nonce write/read proof, two distinct
reviewed ingress nodes, public HTTPS `/version` and readiness probes, and the
ordered `prepare → migration → runtime → exercise` chronology. The foundation
intentionally has no generic live verifier yet; without a configured verifier,
it returns `external_deployment_verifier_unavailable` and records nothing.

Only a fully verified receipt creates an immutable successful staging
`app.deploy` operation and normal deployment history. The bootstrap artifact
signer remains distinct from the normal release signer: only its exact
server-pinned identity may be adopted by a separately protected requalification
workflow, and the signed qualification preserves the bootstrap identity. This
bridge neither changes `DiscoverApps` nor enables normal deployment of any
`deploy: false` app. No capability is advertised until a real verifier is
installed. Retire it after the native translator path is released and proven.

## Durable receipt chain

Before provider mutation, the authority must durably bind:

- target environment, cluster, region, Fleet document path and digest;
- plan UUID, action, current/proposed capacity, reviewed commit, rendered plan
  digest, remote-state backend identity, lineage, and observed serial;
- GitHub PR, protected plan run, server-generated dispatch nonce hash, apply
  run, workflow URL, actor, and verified OIDC identity;
- root attempt, numbered retry lineage, current phase, heartbeat lease, and
  optimistic revision; and
- lifecycle class (`persistent` or `disposable`), owner, and, for disposable
  roots, expiry and cleanup identity.

Every phase appends an attempt-bound checkpoint with the applicable state
serial and evidence digest. The external PostgreSQL database is the online
system of record. A signed receipt export to versioned, retention-protected
object storage is recommended as a second recovery copy; a 14-day workflow
artifact is evidence transport only.

The provider runner still owns state and provider observation. Completion
requires agreement between Git desired state, remote state, the authority's
receipt chain, provider inventory, and runtime health. A dispatch, heartbeat,
state entry, or successful HTTP response alone is insufficient.

## Management networking

Only the regional application load balancer is public. Norn control APIs, SSH,
Nomad, Consul, databases, and observability stay on private VPC addresses and a
reviewed Tailscale management overlay. Tailscale has been selected. The Fleet
integration now has an opt-in private-management contract: private SSH
inventory, HTTPS-only private API naming, authenticated runtime proof, and a
separate public ingress/TLS readiness path. It consumes an already-working
route; Tailscale enrollment, ACL/tag provisioning, HA routing, runner
attachment, and recovery remain open until the bootstrap executor and evidence
checks enforce them.

The bounded implementation should provide:

- environment-specific Tailscale tags and least-privilege ACLs for the Fleet
  authority, protected runners, operators, control nodes, and ingress nodes;
- two subnet-router or gateway instances when routing rather than installing a
  node agent directly, so one failure does not remove recovery access;
- private DNS names for the authority and target Norn API, with trusted TLS;
- protected-runner overlay enrollment from a short-lived or runner-local
  credential, plus strict SSH host-key verification;
- firewall rules that remove public Norn, Nomad, Consul, database, and
  observability ingress and restrict SSH to the overlay or an explicitly
  reviewed break-glass source; and
- readiness evidence from both an operator/runner management path and the real
  public application hostname.

With private management enabled, the Fleet role removes the public Norn route
and exposes only a fixed Traefik ingress-readiness path. That repository change
is not evidence that a tailnet path exists. Provider-console recovery remains
the documented break-glass path when the overlay is unavailable.

## Persistent staging lifecycle

The initial `tk-staging-nyc3` topology remains:

```text
control: 3 fixed voters
ingress: 2 fixed dual-role ingress/workload clients
```

Staging uses dedicated state, database, DNS, overlay tags, OIDC audience,
GitHub environments, release trust, and receipt records. It has no TTL and no
ordinary whole-fleet teardown. Routine changes use the protected plan/apply or
recovery sequence, and drift/cost review compares all five sources of truth.
Optional temporary canary capacity is a future, separately reviewed feature;
it is not part of first bootstrap and must not silently change the fixed pool
bounds.

The bootstrap order is:

1. Qualify the external Fleet authority, its database recovery, signed audit,
   OIDC exchange, and management-network reachability.
2. Validate the exact staging Fleet document and application placement
   locally, without provider credentials.
3. Create the capacity/reconciliation intent in the authority, review and
   merge its deterministic PR, and retain the exact protected plan evidence.
4. Dispatch through the authority. The protected runner creates the numbered
   attempt before provider mutation and records checkpoints for apply,
   inventory, configuration, enrollment, and readiness.
5. Treat first DNS or host-key establishment as an explicit gate. Resume only
   through provenance-bound recovery.
6. Verify private management access, public application ingress, quorum,
   enrolled-node placement, app smoke, durable receipts, provider inventory,
   state, drift, and cost ownership before declaring the pilot ready.

## Optional disposable rehearsals

A disposable rehearsal requires a separate document and root such as
`rehearsals/<id>/<region>`, a unique cluster name, state key, tags, DNS names,
overlay tags, database, receipt namespace, owner, cost center, maximum budget,
and mandatory expiry. The persistent `staging` GitHub environment and state key
must not be reused.

No whole-fleet teardown executor is currently sanctioned. Disposable creation
must remain disabled until a reviewed retirement workflow can:

1. freeze new work and drain/stop workloads;
2. retain signed plan, dispatch, attempt, checkpoint, state, and audit evidence;
3. remove DNS, overlay identities, bootstrap credentials, nodes, load
   balancers, firewalls, volumes, snapshots, databases, object storage, and all
   other tagged resources in dependency order;
4. verify empty managed state and independently query the provider for zero
   matching billable resources; and
5. record a terminal cleanup receipt or a visible incident when either proof is
   unavailable.

Automatic expiry may request retirement, but it must not bypass review,
identity, state-lock, or provider-verification requirements.

## Confirmed owner choices and remaining inputs

The management overlay is Tailscale. The public DNS zone is `betabung.com` and
is currently delegated to DigitalOcean nameservers. Existing DigitalOcean
projects include `theartificial`, `shipt`, and `true-real`; the proposed pilot
uses separate `norn-management` and `norn-staging` projects rather than silently
placing resources into one of those existing projects. Project creation and all
other provider changes still require the reviewed provider lane.

No provider mutation should begin until these values and owners are recorded:

- GitHub owner/repository and repository numeric identity;
- final bootstrap-authority project, region, hostname, availability target,
  and retirement owner;
- external PostgreSQL endpoint, TLS trust, backup/PITR policy, restore drill,
  and receipt-export storage/retention;
- remote-state endpoint, bucket, keys, locking/versioning policy, and recovery
  owner;
- staging public application hostname under `betabung.com` and private
  authority/runtime names;
- Tailscale tailnet, OAuth or auth-key custody, tags, ACL owner, subnet routes,
  and break-glass access owner;
- protected-runner location, labels, stable egress if still needed, host keys,
  and lifecycle owner;
- cloud project, scoped provider token custody, SSH key fingerprints, and cost
  alerts;
- GitHub App identifiers/key custody and staging OIDC audience;
- release repository/trust keys and registry pull posture; and
- any future rehearsal TTL, budget ceiling, cost owner, and authorized cleanup
  approver.

Until those inputs are recorded, the authority release/deployment and
management bootstrap are qualified, and a real retirement executor exists,
the repository contains readiness building blocks rather than a deployed
hands-off pilot.
