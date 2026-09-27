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
   index behavior passed against disposable Nomad 2.0.7; no supervisor calls
   it yet.
   Readback distinguishes a 404 from the current Nomad job revision carrying
   the expected app, deployment, operation, execution, and digest markers.
   An experimental full-job JSON hash failed readback against that same Nomad:
   the server populated job and task-group defaults and runtime fields absent
   from the submitted shape. The experiment was removed. A digest projection
   must normalize those fields while covering every mutable workload field,
   including environment, templates, volumes, networking, and task config;
   a marker-only or source-spec digest is insufficient. The replacement
   registration hashes the complete submitted JSON before writing and saves
   that exact source in Nomad's versioned submission record. Readback checks
   the source digest and markers, asks Nomad to plan that source against the
   current job with no diff, then rereads the revision. A deliberately changed
   workload with matching markers and submission source was rejected by the
   plan against disposable Nomad 2.0.7. This covers the tested raw-exec and
   translated service shapes, not every app dialect, and no supervisor calls
   the primitive yet. Allocation health, effect settlement, and app-gate
   release remain separate work.
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
