# Etcd app deployment admission sequence — 2026-09-26

Status: implementation contract for the next M4 slice. App deployment on
the normal etcd Fleet router is still unsupported. This review follows the
current source and a passing `go test ./etcdstore -count=1` against a
disposable real etcd member at `127.0.0.1:14675`; the member was stopped and
its ports were free afterward.

## Current boundary

`V3OperationStore.Accept` rejects any deployment/region aggregate or
`OneActiveMutablePerApp` request before a write. The normal etcd router has no
ordinary app deployment route and responds with `backend_route_unsupported`.
The PG acceptance transaction already persists a signed intent, operation,
deployment, and regions together after checking that no queued/running
operation exists for that app. The etcd adapter now shares the domain
normalizer, but it has no corresponding atomic persistence or lifecycle index.

## Required implementation order

1. Add an app-scoped active-operation index covering **every** queued/running
   app mutation admitted through etcd, including the canary preview. Its
   condition must be checked in the same etcd transaction that creates the
   signed acceptance, operation, deployment and region records. A completed
   operation may release or replace the active pointer only with a revision
   check against its terminal operation record. An unresolved external effect
   must retain the gate. Bound index growth and reject missing/corrupt links.
2. Persist deployment and region rows atomically with acceptance. On
   ambiguous transaction responses, resolve by the same request identity and
   verify the signed intent plus the exact immutable deployment/region fields.
   A partial or changed domain record is a signature/integrity failure, not a
   replay success. Preserve the idempotency conflict and expiry policy; do
   not make a mutable deploy identity expire while its effect or result is
   unresolved.
3. Implement claim-fenced deployment steps, region observations and terminal
   result writes. A terminal operation, deployment result and app-gate release
   need one proven ordering. Candidate startup and stale claim generation must
   be unable to submit or complete a newer deployment.
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
