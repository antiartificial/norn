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

The stable request selection has a versioned canonical digest over the exact
qualification ID and requested target replica count. The producer computes it
from those caller selections before any mutable read so a retry can resolve
first. On first acceptance, the store recomputes the digest from the resolved
signed qualification and validated desired plan and rejects any mismatch.
Source/spec/image, scheduler lineage, capacity observations, and encrypted
input bytes are deliberately excluded: these are resolved and pinned by the
first acceptance, then must not prevent exact replay if they later change.

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

The pipeline now has a `WorkloadReleasePlan` → observed staged-target binding → strict `WorkloadReleaseIntent` resolver. The plan refuses scheduler version and modify-index values; the binding accepts only a successful plan-derived create-only Nomad job observation, binds it to target operation/execution/job digest and claim generation, and the resolver returns the existing strict intent for downstream health, route, and drain code. The etcd operation store can persist that binding once and reload it only while the signed plan, operation claim, app lock, and Fleet target generation still verify. Deterministic pipeline and etcd integration tests cover mismatch, overwrite, and lost-claim cases.

The claimed release worker now accepts the signed plan shape, reserves the durable stage effect, rebuilds the exact database-free Nomad job from the local InfraSpec, decrypts only operation-bound encrypted managed inputs, stages those inputs, and records the one-use create marker before create-only CAS registration. It ignores the registration response and binds only after exact Nomad source/no-diff readback. Missing or unavailable readback returns pending and never repeats registration; retries and successor claims observe the same durable effect and binding. Deterministic worker tests cover private-input staging failure, claim loss after staging but before the attempt marker, marker transaction failure, and ambiguous registration followed by missing readback; an etcd integration test verifies successor-claim binding through the shared verifier and acceptance-derived stop authorization. Existing health, route, drain, and stop readers resolve the write-once binding to the same strict intent contract. The full API Go suite passes locally. This worker path is not yet an end-to-end release lane: authenticated API admission, automatic qualification discovery, managed catalog/secret revision resolution, and delayed-request/production-driver qualification remain open. Route-predecessor acceptance fences and startup retained-key preflight are implemented; key cleanup and lineage-reader migration remain open.

The private etcd acceptance boundary now takes a configured `nomad.ActiveDeploymentLineageObserver`. Active route records retain operation ID, spec digest, image, process, and job digest from validated deployment effects; workload-release route commits advance those fields from the accepted staged target. Admission rejects an absent observer, compares these control-owned fields with the signed predecessor, then checks Nomad's job ID, version, modify index, replica count, and exact allocation ID set. It repeats the observation immediately before the atomic acceptance transaction, which also compares active-route and reservation revisions. Focused disposable-etcd coverage verifies exact match, stale modify-index rejection, missing-observer rejection, and idempotent replay without fresh mutable reads. The authenticated API/CLI producer still does not exist, so no operator request currently supplies the configured observer to this boundary. Production Nomad/ingress qualification is also open.

Existing active-route records written before the job-digest field was introduced have no trusted digest and therefore fail workload-release admission closed. A verified route refresh/reconciliation must populate that field from its completed deployment effect before those apps can enter the release lane; do not backfill it from Nomad metadata alone.

The API module was rerun with `go test ./... -count=1`; all packages passed. Since no etcd endpoint was configured for that suite, its endpoint-gated integration tests were skipped. Focused runs against disposable local etcd passed qualification source resolution, atomic encrypted-input acceptance/replay, receipt CAS, and staged-target write-once successor-claim binding. The latter was rerun on 2026-11-08 and exercised claim transfer, rejection of not-found readback, exact target adoption by the successor generation, and write-once binding. This is store-layer evidence only; it does not substitute for a production Nomad/ingress driver or an authenticated admission endpoint.

Recovery constraint: after a lost Nomad create response, a successor claim uses the same durable stage effect and one-use attempt marker to observe and bind the exact target. A missing or ambiguous readback cannot authorize another create; the operation remains pending while the old route stays active. The etcd integration test exercises claim transfer, rejects a not-found target, and proves that a matching target binds under the successor generation. Worker orchestration is now connected locally; Nomad delayed-request and production-driver qualification remain open.

## Current input-provenance inventory

The repository has useful candidate and deployment evidence. The etcd/V3
admission seam now corroborates signed qualification data against its own
deployment records. The store can resolve one qualification by exact ID from
its signed operation record, verify the trusted staging signature and expiry,
and require its matching deployment and regions to be complete. No
authenticated workload-release admission endpoint consumes that resolver yet;
automatic candidate discovery and complete operator-facing source authority
remain open:

- `model.ReleaseQualification` binds app, staging deployment ID, source SHA,
  content-addressed artifact, expiry, candidate identity, and staging signer.
  The existing release handler emits and verifies it; normal release queueing
  stores the qualification in deployment operation metadata. The relevant
  paths are `v2/api/model/release.go` and `v2/api/handler/releases.go`. The
  etcd workload-release acceptance verifies the signature, resolves its
  deployment ID through the signed V3 acceptance index, and requires a
  succeeded staging deployment with matching app, source, immutable image,
  spec digest, and completed region results. No V3 route yet discovers and
  supplies a current qualification automatically.
- Qualification source verification must use the verified `GetDeployment`
  projection because the immutable accepted deployment record does not carry
  mutable region-result state. An offline etcd integration test exposed and
  fixed a prior check that read the immutable record directly and therefore
  rejected every otherwise valid qualification as having no completed
  regions.
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
- `secrets.Manager.SnapshotEncrypted` now reads the exact owner-only SOPS
  ciphertext file without decrypting it and returns a content digest. It rejects
  symlinked app/file inputs, wrong ownership, weak file permissions, empty or
  oversized files. `secrets.Manager.Set` now installs encrypted output by
  feeding plaintext directly to SOPS over stdin, then a synced encrypted-file
  rename, so no plaintext temp file remains and readers see the previous or
  replacement ciphertext rather than an in-place partial rewrite. This is a
  safe input capture primitive, not durable release storage. The distinct
  `WorkloadReleaseInputKeyRing` now seals these exact bytes in an authenticated
  envelope bound to authority, operation, app, and plan digest, with its own
  key namespace, rotation support, and retained-key preflight API. The V3
  acceptance now atomically retains the authenticated private record alongside
  signed admission. Replay binds the stable caller selection (qualification ID
  and requested replica count) and returns the original acceptance before
  reading current SOPS bytes, source files, or Nomad state. A changed
  qualification or requested count conflicts; a secret rotation after
  acceptance does not replace the snapshot the worker will use. The retention
  lifetime and deletion policy still need an explicit contract.
- Nomad job version, modify index, job digest, and observed allocation
  identities are scheduler observations. The current strict release intent
  requires them for execution, while the proposal correctly defers constructing
  it until a private target has been read back and write-once bound.

Therefore M5-B is **partially implemented**. Signed qualification lookup and
V3 deployment corroboration, plus encrypted SOPS snapshot retention, are
present at the store seam. The remaining admission adapter must obtain a
qualification ID from trusted control state, resolve catalog and secret
revisions without copying secret values into the signed plan, and fail closed
when a required input lacks a portable immutable reference. Do not silently
treat a legacy PostgreSQL qualification row as an etcd/V3 record.

The source audit on 2026-11-09 narrowed this gate: the normal V3 Fleet router
does not mount the legacy `Handler.CreateReleaseQualification` route. The
legacy `/v1/apps/{id}/qualifications` reader uses `h.db` (PostgreSQL), while
`ResolveWorkloadReleaseQualification` reads a signed V3 etcd operation index.
The store resolver and its integration fixtures therefore do not establish a
production V3 qualification issuer. An opt-in staging-only V3 issuer now
accepts a deployment ID, resolves the signed V3 `app.deploy` record and its
completed deployment projection, takes source/artifact/candidate provenance
from that record, signs a qualification, and stores it as a signed
`release.qualification` operation. Its replay path returns the accepted
receipt before rereading source state; concurrent same-deployment requests
resolve the first committed receipt. The route requires
`NORN_ETCD_FLEET_QUALIFICATION_HTTP=true` alongside the existing V3 staging
release and deploy-worker configuration. Deterministic tests cover source
binding, replay, changed deployment IDs, malformed JSON, and signature
self-verification; an etcd-backed integration test verifies lookup through the
signed acceptance index. This source is not yet deployed or live-qualified.

Admission must derive the target job digest before it can sign the complete
`WorkloadReleasePlan`; it must not duplicate the translator or accept a
caller-provided digest. This is now implemented: the shared target builder
lives beside the release contract in
`pipeline.BuildWorkloadReleaseTargetJobPlan`; the worker delegates to it, and
admission can call `pipeline.DeriveWorkloadReleaseTargetJobDigest` with an
intent that has no target digest. Derivation rejects a prefilled digest and
reuses the exact translator and input-plan validation used again by the
worker. Focused pipeline/worker tests and the full `go test ./...` API module
suite passed on 2026-11-09. This closes the digest-construction cycle only; it
does not add the authenticated operator producer or qualify live drivers.

For a same-key retry, resolve the signed request identity and compare a
server-computed digest of stable caller-selected fields before reading mutable
source, encrypted inputs, or Nomad state. The private store now requires that
digest and returns the signed prior acceptance when it matches. Changed
qualification or requested replica count conflicts; a changed current SOPS
snapshot does not change an already accepted request. After concurrent
requests race, resolve the signed identity again and return the first committed
operation when the stable request digest matches, even if the losing request
observed newer target IDs or captured newer encrypted bytes.

### Secret input contract required before admission

The current workload SOPS manager reads and rewrites a mutable filesystem path.
Hashing the decrypted map is undesirable: it puts a stable password-derived
value in public integrity metadata. Hashing only the SOPS file ciphertext is
not necessarily safe as public metadata if the document contains unencrypted
fields, and it does not make those bytes recoverable after a host or repository
change. The admission contract must therefore bind an opaque, immutable
private-record ID and the digest of its authenticated envelope, with an
availability/retention guarantee for the operation's recovery lifetime.

The signed release plan should contain only a reference such as
`{privateInputRecordId, envelopeDigest, schemaVersion}`. The exact SOPS bytes
belong inside the operation-bound private envelope; do not put the raw secret
file digest in public signed metadata because SOPS documents may contain
unencrypted metadata. Before accepting the operation, the controller must
prove that the exact encrypted input is durably retrievable. A worker restart
must reopen that immutable ciphertext, decrypt it using the configured SOPS
key authority, and validate the envelope reference before constructing the
Nomad target. Never put decrypted values in the signed plan, operation payload,
logs, test snapshots, or public receipts.
If the source cannot guarantee immutable retrieval, either add a private
encrypted snapshot store with key rotation/retention semantics or keep this
candidate ineligible for hands-free release. A local pathname or current
secret-manager lookup is not sufficient recovery evidence.

Database credentials follow the same principle: the existing catalog revision
and exact binding payload digest are useful nonsecret identity, but every
referenced credential document must also resolve from a stable retained
revision. Re-resolving a mutable `secret:` path after acceptance must fail the
release before stage or cutover rather than silently rotate the workload input.

The etcd private-invocation envelope is a precedent for authenticated
encryption, operation-bound AAD, atomic acceptance, retained-key inventory,
and startup key preflight. Its function-specific key ring and purpose are not
the workload-release key boundary. A release-input implementation needs a
separate key namespace and AAD purpose, but should preserve those durable
properties. Bind the ciphertext to authority, operation ID, app, and plan
digest. Persist it atomically with signed release acceptance so a request-key
replay returns the original operation before reading the current secret file.
Retain the envelope and its decryption key while the stage effect is unresolved
or held; allow deletion only after exact target binding or an independently
proved no-effect terminal outcome. Startup/restore preflight must require keys
for every retained release envelope. A successful target binding should keep
only the nonsecret ciphertext digest and source revision in the public receipt.

The existing `PrivateInvocationStore` remains function-only: it accepts
`function.invoke`, uses its own key-ring config, and scans its own records for
key requirements. The workload-release envelope is a separate store-level
primitive with an etcd record, signed-plan-bound atomic acceptance,
same-material replay comparison, and retained-key inventory scan. The etcd
Fleet deploy runtime now checks that inventory against the configured release
key ring before starting its worker. Retention cleanup remains unimplemented;
this is explicit work, not behavior inherited from the function path.

## Open questions and risks

- Which existing qualified-candidate record is authoritative for binding source, spec, and image to the same immutable build?
- Which managed secret, catalog, and database inputs expose immutable revisions that can be reconstructed after a worker restart?
- Do all app, scale, deploy, Fleet, and route writers honor the same app mutation reservation?
- Can Nomad ACL and retention policy keep a target job identity reserved while the accepted release is unresolved?
- Which recovery action may safely clear a held release whose submission marker exists but whose target is absent?
- What separate evidence and owner will authorize stateful mixed-version workloads?

## Next implementation step

The private-input materialization and validated create/readback path are now wired into the claimed worker locally; do not treat that as deployed qualification. The next implementation step is an authenticated operator admission producer that resolves the newly issuable V3 qualification and then obtains server-owned predecessor, current Fleet target, serving-lineage and encrypted-input facts before atomically admitting the signed plan and private envelope. In parallel, extend deterministic fixtures across marker, delayed Nomad acceptance, response loss, successor claim, target readback, and durable hold. Then run the same path against version-pinned disposable Nomad and ingress with the configured ACL policy. Keep route-predecessor fencing, startup key preflight, and explicit envelope retention; do not enable provider or workload mutation until those gates pass.
