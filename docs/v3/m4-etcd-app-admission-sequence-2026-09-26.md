# Etcd app deployment admission sequence — 2026-09-26

Status: implementation contract for M4. App deployment on the normal etcd
Fleet router is still unsupported. The first app-index slice was verified
against disposable real etcd at `127.0.0.1:14679` on 2026-09-26.

## Current boundary

The public `V3OperationStore.Accept` still rejects any deployment/region
aggregate before a write. A private preparation path now atomically persists
the signed operation, deployment, resolved regions, and app gate for real-etcd
contract tests; it is not exposed to the API or worker. A private terminal
transaction now writes deployment and region results with a live claim and app
lock, and releases the app gate only with the terminal operation. Generic
operation completion refuses accepted deployments. A signed-identity lookup
reconstructs queued and terminal deployment views from etcd, and replay
rejects missing or changed terminal region results. The store indexes
queued app operations, enforces exclusive app admission in the acceptance
transaction, and releases the index in claim-fenced terminal
transactions. Private invocation acceptance and completion participate, and
the canary preview requests exclusive admission. A terminal operation with
manual or external-effect recovery pending retains the index. The normal etcd
router has no ordinary app deployment route and responds with
`backend_route_unsupported`.

The PG acceptance transaction already persists a signed intent, operation,
deployment, and regions together after checking that no queued/running
operation exists for that app. The etcd adapter now shares the domain
normalizer, atomic deployment persistence, and a private terminal projection;
intermediate deployment stages, Nomad launch/reconciliation, and the normal
router remain unwired. The index initializes once per app only after a snapshot scan finds
no pre-index active or unresolved operations. Mixed-version
API writers must be stopped before enabling the adapter; an upgrade retaining
active pre-index work must first drain or reconcile it.

## Required implementation order

1. Complete and harden the app-scoped active-operation index covering **every** queued/running
   app mutation admitted through etcd, including the canary preview. Its
   condition must be checked in the same etcd transaction that creates the
   signed acceptance, operation, deployment and region records. A completed
   operation may release or replace the active pointer only with a revision
   check against its terminal operation record. An unresolved external effect
   must retain the gate. Bound index growth and reject missing/corrupt links.
   Two-client exclusive acceptance and pre-index active-operation rejection
   are now covered by real-etcd tests; mixed-version writes remain outside
   the supported transition.
2. Persist deployment and region rows atomically with acceptance. The private
   preparation path and signature-verified replay now pass a real-etcd test;
   the public path remains disabled until execution is available. On
   ambiguous transaction responses, resolve by the same request identity and
   verify the signed intent plus the exact immutable deployment/region fields.
   A partial or changed domain record is a signature/integrity failure, not a
   replay success. Preserve the idempotency conflict and expiry policy; do
   not make a mutable deploy identity expire while its effect or result is
   unresolved.
3. Implement claim-fenced deployment steps, region observations and terminal
   result writes. The private terminal transaction now writes all results and
   the app-gate release in one ordering; stale claim and lost app-lock tests
   pass against real etcd. Intermediate checkpoints, effect verification, and
   worker recovery remain open. Candidate startup must be unable to submit a
   newer deployment while an older Nomad effect is unresolved.
   A private deploy effect reservation now binds the signed deployment,
   accepted region, pinned image, spec digest, and job digest to the shared
   etcd app effect gate. Its lifecycle passes a real-etcd test, but a Nomad
   supervisor has not yet proven the submitted job or recovery observation.
   A Nomad create/update registration primitive now uses an expected job
   modify index, validates the app and execution markers before sending, and
   classifies ambiguous responses as indeterminate. Its create-only and stale
   index behavior passed against disposable Nomad 2.0.7; the private worker
   calls it, while normal claim dispatch does not.
   Readback distinguishes a 404 from the current Nomad job revision carrying
   the expected app, deployment, operation, execution, and digest markers.
   An experimental hash of Nomad's rendered job failed because the server
   populated defaults and runtime fields absent from the submitted shape.
   That experiment was removed. A marker-only or source-spec digest would
   miss mutable workload fields. The replacement
   registration hashes the submitted JSON before writing, excluding only the
   digest's own marker and its digest-derived execution marker, then saves
   the full source in Nomad's versioned submission record. Both excluded
   markers are checked separately against the reserved execution. Readback checks
   the source digest and markers, asks Nomad to plan that source against the
   current job with no diff, then rereads the revision. A deliberately changed
   workload with matching markers and submission source was rejected by the
   plan against disposable Nomad 2.0.7. This covers the tested raw-exec and
   translated service shapes, not every app dialect. Allocation health is available to the private worker;
   signed region checkpoint and deployment-result writes remain separate work.
   Nomad calls the submitted source reference data, retains only the latest
   six job source files, and does not schedule from it. Missing source must
   leave recovery indeterminate; the separate no-diff plan is required.
   Since the source can include task environment and templates, use the same
   restricted Nomad job-data boundary as the live job and never copy it to
   Norn logs or release evidence. The worker also needs Nomad `plan-job` or
   `submit-job` permission for recovery; check that capability before
   enabling admission. See the [Nomad Jobs API](https://developer.hashicorp.com/nomad/api-docs/jobs).
   The etcd deployment effect store now has a separate claim-fenced,
   create-once submit-attempt marker. Two callers cannot both receive write
   authorization, and a lost operation lease cannot mark an attempt; both
   passed disposable real-etcd tests. A marked attempt followed by Nomad 404
   remains unresolved and must never auto-resubmit. The marker is not wired
   into a supervisor yet. Manual resolution still needs a durable revocation
   or a Nomad revision barrier that defeats a paused old submitter before the
   app gate can be released.
   A private worker step now binds a translated service job and pinned image
   to that reservation, marks the one allowed submit attempt, calls Nomad CAS,
   and reconciles via the versioned readback. Replay and recovery never
   resubmit; an attempted 404 stays unresolved. The step passes worker tests,
   while its etcd store dependencies pass disposable real-etcd tests. It is
   not connected to normal claim dispatch, deployment stage checkpoints,
   or terminal result writes. Periodic jobs and other
   deployment shapes remain outside this one service-job step.
   A separate read-only health observation now requires the exact versioned
   job readback, a pinned image, every declared group's desired count of
   running healthy allocations, matching allocation job snapshots, and a
   stable final job revision. Old terminal allocation history is ignored;
   live work from a removed group or changed provenance fails closed.
   A disposable Nomad 2.0.7 Docker service reached `ready` with one exact
   allocation, and the test job was purged. A private worker completion step
   now consumes `ready` only for the original launched effect, recorded submit
   attempt, and exact job revision; pending, unavailable, or changed revision
   keeps the effect gate. The completion evidence binds the reserved input,
   execution, job digest, version and healthy allocation IDs. This worker
   path has unit coverage and passed a disposable Nomad 2.0.7 Docker worker
   sequence from guarded submit through exact health completion. A second
   disposable test now passes the same sequence with signed private etcd
   admission, a live claim and durable effect record, real Nomad 2.0.7, and
   a healthy Docker allocation. It verifies replay keeps the same effect,
   completion releases its gate, and the deployment operation remains active.
   This caught and corrected a circular job digest/execution-ID dependency.
   The same private test now carries the completed effect through the
   claim- and lock-fenced terminal transaction, verifies signed-identity
   replay and the region projection, and checks that terminalization releases
   app admission. The terminal writer now refuses success or ordinary failure
   while the app effect gate is unresolved, with a no-gate check in its etcd
   compare transaction; success cannot use a recovery-pending flag to bypass
   that fence. The test constructs the terminal region result after health;
   a production worker still needs to derive and persist that result from
   verified evidence. The translator emits `norn.traffic-weight` as a Consul
   service tag, while the PostgreSQL pipeline writes desired weight after
   Nomad readiness; neither observes effective ingress weight. Identify the
   responsible ingress or traffic controller, then observe its effective
   configuration and endpoint behavior before writing nonzero `ActiveWeight`.
   Normal dispatch remains disabled.
   Canary jobs still require their separate promotion proof.

   A source audit on 2026-09-27 checked the exact Fleet PR #176 head
   `6267655052b209b22dc8b3421cb9339f797af9bc`: Traefik runs on each
   ingress node with the Consul Catalog provider, and the DigitalOcean load
   balancer forwards to those ingress nodes. The checked Fleet templates have
   no renderer, API route, or controller consuming `norn.traffic-weight` as a
   Traefik weighted service. The load balancer's target pool is ingress nodes,
   not app deployment revisions. Thus Nomad health, Consul tags, and the
   load-balancer health check cannot establish an effective app traffic
   percentage. Traefik's documented Consul Catalog
   [`loadbalancer.server.weight`](https://doc.traefik.io/traefik/master/reference/routing-configuration/other-providers/consul-catalog/)
   changes a server's weight within a service; adding that tag to every
   allocation in one region would not by itself implement Norn's desired
   deployment/region percentage. The next implementation slice must name one traffic authority,
   apply the accepted deployment's desired weight there with a durable
   revision, read back the resulting route on **each** ingress node, and
   probe the intended app endpoint through those nodes and the public ingress.
   Record the observed revision and endpoint identity with the region result
   before setting positive `ActiveWeight`; partial propagation stays pending.
   Multi-region or canary percentages also need explicit weighted routing
   semantics and rollback evidence rather than interpreting service tags as
   routing policy.

   A 2026-09-27 follow-up checked Fleet PR #176 head `6267655052b209b22dc8b3421cb9339f797af9bc`
   `ansible/templates/traefik.yml.j2`: the file provider already watches
   `/etc/traefik/dynamic`, beside Consul Catalog. Traefik's
   [file provider](https://doc.traefik.io/traefik/reference/routing-configuration/other-providers/file/)
   can define a weighted service, and its
   [provider namespaces](https://doc.traefik.io/traefik/reference/install-configuration/providers/overview/)
   allow an explicit reference to a Consul Catalog service. This is a
   candidate authority, not an applied Norn route. The current translator
   registers each process under the stable Consul service name `app-process`
   and emits a hostname router from its tags. It does not separate old and
   new deployment revisions into independently addressable services. Before
   rendering a weighted file route, give each revision a stable distinct
   backend identity and remove competing hostname routers for that managed
   endpoint. Keep the legacy route path until a guarded v3 transition has
   readback and rollback. A file write alone cannot count as propagation:
   the controller must atomically publish one versioned route on every
   ingress node, verify Traefik's effective router/service on each node,
   and probe the endpoint through each node and the public path. A partial
   rollout or a backend whose revision identity cannot be read back keeps
   `ActiveWeight` at zero.

   The first opt-in translator slice, `TranslateForManagedDeployment`, now
   derives distinct, deterministic Nomad job and Consul service names for
   each deployment revision. Its service
   tags use a reserved `.norn.invalid` hostname instead of the public
   endpoint and omit the misleading desired-weight tag. An unmatched regional
   endpoint remains private. The normal v2 translator and private worker
   still use their existing paths; this helper does not apply a weighted
   file route, observe Traefik, or prove a deployment's active weight.
   A correction after the local Traefik fixture found that one stable Nomad
   job ID cannot sustain old/new weighted backends: its normal update replaces
   old allocations, while the current private health verifier requires all
   live allocations to carry the new job version. Revision-specific jobs
   keep both backends addressable until the route moves and the old job is
   explicitly drained. Job-scoped Nomad variable templates now follow the
   revision job ID. The private CAS/readback/health path now accepts an
   optional job ID only when it matches the accepted app, logical region,
   and deployment ID and uses create-only Nomad CAS. Readback, allocation
   health, and completion evidence follow that job ID; legacy callers retain
   their app job lookup. Local fake-Nomad tests cover two simultaneously
   addressable revision jobs and the worker's completion provenance. The
   normal etcd dispatch still does not construct this managed job/effect,
   and job-scoped database/secret delivery, protected Nomad concurrency,
   ingress publication, and fenced old-job retirement remain required.
   The managed translator now rejects a runtime-database job without a
   positive staged catalog revision and renders the exact revision under
   `nomad/jobs/<revision-job-id>`. Existing checked Nomad Variable delivery
   can keep old and new revision job paths independent in a fake API test.
   A private managed-job input plan now enumerates the exact revision-scoped
   variable keys. The worker rejects a plan that omits a rendered template
   key or staged database target, then reads the job Variable and compares
   delivered target identity before reserving a Nomad effect. A constructor
   binds this plan to the accepted deployment's spec digest and typed target
   identities. The etcd effect reservation now compares the plan's catalog
   revision and every expected runtime target to the signed
   `databaseTargets` acceptance payload before reserving the effect. The
   private Nomad Variable delivery can now CAS-add exactly the planned
   process secret file keys to that revision job without overwriting staged
   database material; a different value at the same job ID is refused. A
   private source adapter selects only those keys from the app secret map and
   refuses missing values before any write. The normal etcd dispatch still
   must resolve the accepted targets, obtain their probed connection material,
   and invoke the private delivery path. That path now refuses missing or extra
   runtime database items and a target identity that differs from the signed
   plan before staging the revision. A private preparation entry point now
   combines the descriptor constructor, rendered job, secret source, exact
   database items, both checked deliveries and a readback. It preflights all
   local inputs before either write. Dispatch still must resolve and probe
   the accepted targets and invoke that entry point; no normal managed job
   path is enabled by this private preparation alone.
   Etcd now has a read-only claimed-deployment verifier that follows the
   acceptance index, rechecks the retained signature and immutable deployment
   aggregate, and compares the claimed payload. It passed against a disposable
   local etcd member, including forged app and payload refusals. A dedicated
   deployment worker must call it while holding a live claim and app lock;
   the existing general `app.deploy` worker still enters the PG pipeline.
   A private worker helper now derives the runtime-only target map and catalog
   revision from that verified acceptance plus the pinned InfraSpec digest;
   migration-only or snapshot-only signed databases do not become runtime
   delivery keys. It still needs a resolver that reopens and probes those
   exact targets before Nomad preparation.

   The next opt-in `ingress.RenderWeightedRoute` slice emits one file-provider
   hostname router and a weighted service referencing those exact
   `@consulcatalog` backend names. It requires an origin URL, distinct
   deployment IDs, positive weights totaling 100, and returns a SHA-256 of
   canonical YAML. Input order cannot change that desired-route revision.
   Package tests cover both-revision routing and unsafe or incomplete plans.
   The renderer is not connected to a privileged file publisher, Traefik
   readback, or endpoint probes, so it supplies no effective-weight evidence.

   `ingress.ObserveRenderedRoute` now reads Traefik's `/api/rawdata` on every
   explicitly named node and compares the enabled `@file` router, TLS mode,
   weighted service, and enabled `@consulcatalog` backends against the rendered
   route. It rejects partial propagation, another enabled router claiming the
   same public host, configuration errors, changed weights, and API redirects.
   This is a read-only config observer with local HTTP fixtures; Fleet's
   checked Traefik template has `api.dashboard: false` and no management
   router exposing `api@internal`, so no live node has been observed through
   this path. A protected local or authenticated private API access path must
   be added and qualified before use. Even a matching `/api/rawdata` response
   must be paired with per-node and public endpoint probes before recording
   positive `ActiveWeight`.
   A [disposable Traefik/Consul runtime fixture](m4-local-traefik-weighted-route-fixture-2026-09-27.md)
   subsequently loaded the rendered route, passed actual `/api/rawdata`
   observation, and returned 70 old / 30 new responses over 100 local
   requests. That validates this local composition, not Fleet propagation.
4. Move the deploy pipeline's direct `*store.DB` dependencies behind explicit
   domain interfaces, then wire the normal etcd router and worker. Admission
   must reject an unavailable build, database binding, secret delivery,
   archive, or Nomad effect capability before accepting a request it cannot
   execute. Advertise the route only after the complete path works without a
   usable control PostgreSQL connection.

## Acceptance tests for the slice

- Two APIs race to accept different mutable operations for one app: exactly
  one wins; replay of the winner returns its original signed operation and
  deployment IDs.
- A lost response after commit replays the complete original aggregate. A
  changed request key/fingerprint, missing region, altered deployment field,
  or corrupted index fails closed without a second Nomad submission.
- A worker dies before launch, after ambiguous launch, and after verified
  Nomad success but before terminal persistence. Recovery preserves the
  original identity and never launches an unresolved effect twice.
- The app gate remains while work is queued/running or its external effect is
  unresolved. Terminal success/failure and a separately verified resolution
  release it without admitting an overlapping stale claim.
- A normal production-mode etcd API with a poisoned control-PG URL accepts,
  executes and reads one disposable app deployment through Nomad; archive,
  auth, database binding and route capability are checked through that path.

These tests are prerequisites to M4 source qualification. Protected
separate-host Fleet bootstrap, placement, drain, and M7 migration remain
separate release gates.
