# M5 workload release admission

## Problem

The workload-release worker already owns health, weighted routing, and old-job stop recovery, but its signed `WorkloadReleaseIntent` requires the target Nomad version and modify index before the target job exists. Ordinary `app.deploy` cannot be used as a staging step because its successful path publishes the public route. Admission must own a private target create and then enter the strict existing release protocol without treating caller input as scheduler evidence.

## Usage (caller's view)

An operator selects one already-qualified immutable candidate and supplies an idempotency key. The controller derives serving lineage, target identity, placement, capacity policy, and route predecessor. One accepted operation owns staging through completion; callers do not coordinate separate stage and release operations.

```text
norn app release <app> --candidate <qualified-candidate> --request-key <key>
norn operation get <operation-id>
```

The same key and request return the original operation. Reusing the key with a different candidate is a conflict. Scheduler indices, old allocation IDs, route weights, arbitrary Nomad jobs, and compatibility flags are not accepted from the caller.

## Shape

Represent immutable authorization and observed scheduler identity as different records:

```go
type ReleasePlan struct {
    // Signed, immutable desired source/spec/image, serving lineage, target job
    // capsule, Fleet generation, route predecessor, capacity and policy.
    // Contains no target Nomad version or modify index.
}

type StagedTarget struct {
    PlanDigest, EffectID, JobID, ExecutionID string
    JobVersion, JobModifyIndex uint64
    JobDigest, ObservationDigest string
}

// The release store verifies the signed plan and immutable stage binding,
// then constructs the strict WorkloadReleaseIntent consumed by route/stop code.
func ResolveClaimedRelease(ctx context.Context, claim Claim, appLock AppLock) (BoundRelease, error)
```

The API admission resolves request replay before current-state reads, then atomically fences and records request identity, signed plan, operation, app mutation slot, active route predecessor/reservation, completed old lineage, candidate/catalog inputs, desired count, and Fleet target generation. Remote Nomad and ingress observations are rechecked at side-effect boundaries; they are not described as part of the etcd transaction.

The claimed release worker stages one deployment-specific job with a stable job and execution identity. It uses exact source/image/input digests and secret-free, versioned input references. Public route state remains unchanged during staging. After exact marker/source/no-diff readback, the worker writes a write-once target binding under the current claim and derives the strict runtime intent from plan plus binding. All health, route, drain, scale-lineage, and completed-release readers use that same resolver. The current target-only route proof and full target health requirement before old-job drain remain intact.

The initial admission is limited to an owner-approved stateless overlap policy. A generic stateless flag is not evidence of data compatibility. Stateful workloads require a separate compatibility authorization covering writers, schema, and acknowledged data.

## Synthesis decision

The three independent sketches converge on one release operation with private staging, a signed desired plan, a write-once observed target binding, and a derived strict execution value. Use the shared shape. Take Astra's single resolver and reader-migration map, Sol's idempotency and failure matrix, and Terra's explicit capacity and policy limits. Reject public `app.stage`, `app.deploy --skip-route`, and optional/unset target indices in the existing intent because they expose internal sequencing or weaken downstream invariants.

For the first implementation, preserve the existing conservative recovery rule: after a durable submit-attempt marker, a successor may observe and bind the exact job but must hold if readback is absent or ambiguous. Nomad's documented zero-index compare-and-set and current disposable test establish create-only behavior while the job exists, but do not qualify delayed requests across purge/recreation or the deployed scheduler/ACL/retention configuration. A later automatic retry protocol needs version-pinned Nomad qualification, enforced retention, and concurrency/crash tests.

## Tradeoffs accepted

- We accept two immutable records for desired target and observed target in exchange for never signing a guessed scheduler index.
- We accept a hold after the attempt-marker/pre-send crash gap in exchange for never treating absence as proof that no request can still land.
- We accept requiring full old-plus-target capacity during overlap in exchange for keeping the old workload available until target-only routing is proved.
- We accept stateless-only first admission in exchange for keeping data compatibility claims out of generic health and routing evidence.
- We accept adapting every route, stop, and scale lineage reader to one resolver in exchange for a single authoritative interpretation of the release.

## Alternatives considered

- **Public staging operation followed by release:** callers would coordinate two idempotency lifecycles and a gap in which serving lineage or route state can change.
- **An ordinary deploy with a route-suppression flag:** this changes deploy's success meaning and risks creating a second route authority.
- **Optional target scheduler fields in the existing intent:** downstream consumers would need to distinguish planned from executable intents at every read.
- **Automatic resubmission after attempt marker and 404:** current evidence does not establish safety across deletion, purge, or an in-flight delayed request.

## Implementation reconciliation

No runtime code has been changed to implement this design yet. Existing `WorkloadReleaseIntent` validation and its target-only-before-drain behavior remain authoritative. The protocol document's stale mixed-route-before-drain descriptions are corrected in the accompanying documentation change. Input provenance, stage effects, receipt digests, and every lineage reader must migrate as one release contract; a partial adapter is not a completed M5 lane.

## Current input-provenance inventory

The repository has useful candidate and deployment evidence, but these pieces
do not yet form an etcd/V3 workload-release candidate authority:

- `model.ReleaseQualification` binds app, staging deployment ID, source SHA,
  content-addressed artifact, expiry, candidate identity, and staging signer.
  The existing release handler emits and verifies it; normal release queueing
  stores the qualification in deployment operation metadata. The relevant
  paths are `v2/api/model/release.go` and `v2/api/handler/releases.go`. Those
  handlers use the existing `Handler`/DB path; the etcd workload-release
  verifier does not resolve or consume a qualification record.
- The accepted deployment records `CommitSHA`, immutable image reference,
  `SpecDigest`, environment, and regions. `v2/api/etcdstore/v3_deployment_acceptance.go`
  retains the signed acceptance and immutable deployment projection. The
  active serving target is resolved from the published route and its completed
  deployment in `v2/api/etcdstore/v3_scale_target.go`.
- `pipeline.BindDeploymentJobProvenance` binds deployment ID and spec digest
  plus a digest of the exact accepted database binding payload. This is useful
  lineage evidence, but the current release intent has no generic reference to
  an immutable runtime secret revision. `InfraSpec.Secrets` carries names, and
  `secrets.Manager` decrypts the current `secrets.enc.yaml` path; the interface
  has no revision parameter. A name alone is not a restart-stable value/version.
  The canonical spec digest is computed from YAML (including `Env`, which is
  intentionally omitted from JSON), so it pins the spec bytes but does not pin
  the separately encrypted secret file's selected revision.
- Nomad job version, modify index, job digest, and observed allocation
  identities are scheduler observations. The current strict release intent
  requires them for execution, while the proposal correctly defers constructing
  it until a private target has been read back and write-once bound.

Therefore M5-B is **partially inventoried, contract not implemented**. The
first candidate source must be an explicit adapter that verifies the signed
qualification, confirms its successful staging deployment and app/spec
binding, and resolves secret/catalog revisions without copying secret values
into the signed plan. If the current store cannot provide a portable immutable
reference for any required input, admission must refuse that candidate until
the source contract is extended. Do not silently treat the legacy PostgreSQL
qualification path as an etcd/V3 record.

### Secret input contract required before admission

The current workload SOPS manager reads and rewrites a mutable filesystem path.
Hashing the decrypted map is undesirable: it puts a stable password-derived
value in public integrity metadata. Hashing only the ciphertext is safe as an
identifier, but does not make those ciphertext bytes recoverable after a host
or repository change. The admission contract must therefore bind both an
opaque, immutable source revision and the digest of its encrypted bytes, with
an availability/retention guarantee for the operation's recovery lifetime.

The signed release plan should contain only a reference such as
`{sourceRevision, ciphertextDigest, schemaVersion}`. Before accepting the
operation, the controller must prove that the exact encrypted input is
durably retrievable. A worker restart must reopen that immutable ciphertext,
decrypt it using the configured SOPS key authority, and validate the same
reference before constructing the Nomad target. Never put decrypted values in
the signed plan, operation payload, logs, test snapshots, or public receipts.
If the source cannot guarantee immutable retrieval, either add a private
encrypted snapshot store with key rotation/retention semantics or keep this
candidate ineligible for hands-free release. A local pathname or current
secret-manager lookup is not sufficient recovery evidence.

Database credentials follow the same principle: the existing catalog revision
and exact binding payload digest are useful nonsecret identity, but every
referenced credential document must also resolve from a stable retained
revision. Re-resolving a mutable `secret:` path after acceptance must fail the
release before stage or cutover rather than silently rotate the workload input.

## Open questions and risks

- Which existing qualified-candidate record is authoritative for binding source, spec, and image to the same immutable build?
- Which managed secret, catalog, and database inputs expose immutable revisions that can be reconstructed after a worker restart?
- Do all app, scale, deploy, Fleet, and route writers honor the same app mutation reservation?
- Can Nomad ACL and retention policy keep a target job identity reserved while the accepted release is unresolved?
- Which recovery action may safely clear a held release whose submission marker exists but whose target is absent?
- What separate evidence and owner will authorize stateful mixed-version workloads?

## Next implementation step

Implement the signed plan, atomic idempotent admission fences, and one shared plan-plus-binding resolver before wiring private Nomad staging into the existing reconciler.
