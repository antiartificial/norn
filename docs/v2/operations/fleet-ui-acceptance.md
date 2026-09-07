# Fleet UI acceptance

This is the engineer-facing acceptance sequence for a persistent staging
Fleet. A local developer server is a separate connection, not the authority
for the cloud Fleet. Production is not part of this rehearsal.

## Connect safely

Use the private HTTPS **management authority** URL supplied by the platform
owner, while connected to the approved management VPN. Do not use the public
application hostname as the management endpoint unless the owner explicitly
identifies it as such. A saved server connection selects one authority and its
configured Fleet; it is not automatic discovery of every Fleet in the account.

Pair an individual device. The authority permits `api:read` for observation
and, when explicitly requested and approved, `api:write` for planning and
reviewed GitHub handoffs. An administrator confirms the pairing code through
the existing enrollment approval flow. The requested scopes can be reduced
but cannot be expanded during approval. Do not distribute the root API token.

`fleet:operate` is protected runner authority, not the human operator role.
Humans cannot advance checkpoints, heartbeat, or manufacture runner evidence.
Full mutation-audit history is administrator-only; engineers inspect the
durable plan, operation, and runner receipts instead.

The native app stores managed credentials in its credential vault. Browser
pairing uses a session-only in-memory credential: reloading requires pairing
again. Neither approach needs provider, state, SSH, or release-signing secrets.
The built-in browser requests a 30-minute credential and offers explicit
sign-out/revocation; native credentials retain their normal lifetime. Platform
metadata is client-supplied, so enrollment approval remains a trust decision,
not cryptographic proof of a browser identity.

## Engineer walkthrough

1. **Identify the connection.** Confirm the environment and management-only
   role. An available management API does not prove target workload health.
2. **Inspect Fleet.** Read the configured pools and validation findings. Desired
   node counts are configuration, not observed live nodes. Check observation
   freshness; disconnected or stale data must not appear current.
3. **Inspect a change.** Open its durable plan and follow the review, protected
   dispatch, numbered runner attempt, and evidence-backed checkpoints. Closing
   and reopening the client must recover the same plan and history.
4. **Exercise permissions.** A viewer cannot plan or dispatch. An approved
   operator can prepare a plan and use the reviewed GitHub handoff, but still
   cannot bypass review or perform runner mutations.
5. **Observe the workload separately.** Until an authenticated runtime
   association is implemented, use its separately configured runtime connection
   and public test endpoint. The authority has no workload, deployment, event,
   or node-health endpoints; their absence is not a failing runtime check.

## Live sequence and evidence

| Stage | Required evidence |
|---|---|
| Controller and access | Independent durable database, trusted HTTPS, scoped pairing, VPN/ACL checks, protected runner reachable without the developer machine |
| Provision | Reviewed merged SHA and plan digest; provider state and inventory; configured/enrolled nodes; completed readiness checkpoints |
| Workload | Reviewed `hello-norn-mysql` image and catalog; schema readiness; two distinct-host allocations; verified MySQL TLS and synthetic write/read round trip |
| Light load | Bounded `exercise.py` run; latency/error observations; every acknowledged record re-read; metrics visible on private monitoring |
| Release | Exact old/new artifact identities; rollout receipt; endpoint observations; failed-readiness rejection and recovery/rollback receipt |
| One-node fault | Named target and approved fault action; capacity headroom check; surviving endpoint observations; recovery to full health before another fault |
| Close | Load stopped; health restored; test evidence retained; temporary resources absent from state and provider inventory |

The initial three-control/two-client single-region topology tests node-level
resilience, not regional failover. Two distinct-host replicas plus a canary
need three eligible clients; the initial two-client pilot uses rolling updates.
The test UI must never provide an unrestricted failure or deletion button.
Faults require a bounded, reviewed executor and explicit recovery evidence.

Persistent staging intentionally remains online and billable after testing.
Disposable rehearsals require a separate state root and a reviewed retirement
executor before creation. A JSON cleanup-input validator is not proof that
provider resources have been deleted.

## Current activation gates

Local tests and UI fixtures cannot replace a protected live run. Before the
first pilot, finish the independent authority/runner deployment, protected
state configuration, managed database integration, and private bootstrap
transition. In particular, post-create Tailscale node identities and private
routes must be verified before subsequent private phases; merely enabling a
private-management flag cannot resolve a target that does not exist yet.

Run the Fleet repository's setup doctor and retain its findings. Missing
provider/state environment secrets, runner registration, bootstrap/known-hosts
files, or the authority endpoint block live apply. Never use the developer
Mini, fabricated checkpoints, or broadly exposed management routes to bypass
those gates.

## Known full-runtime qualification gap

The pilot candidate removes migration's global `server_restarted` session
reaping. A second database connection running migration now preserves a live
peer's session in integration tests. Abandoned running sessions expire at their
existing authorization deadline (currently at most one hour), including during
new-session quota admission. This is deadline-bounded cleanup, not immediate
owner-lease recovery. The candidate is not deployed, and a real multi-replica
terminal/rollout exercise remains necessary before claiming HA exec qualified.

Database integration suites must use separate databases per concurrent package
or run sequentially. A sequential pass avoids the migration race in the test
setup. The new regression test covers the specific migration side effect; it
does not establish that unrelated concurrent migration/test suites are safe.

## Local acceptance evidence — 2026-09-07

The built web UI was exercised against the actual authority router and a
disposable PostgreSQL database on loopback. Enrollment start, administrator
approval, exchange, viewer Fleet reads, visible durable plan history, hidden
operator controls, and explicit sign-out passed. Revocation returned HTTP 204;
the UI returned to pairing. Only advertised management APIs were requested
during this walkthrough. The harness exited successfully and its disposable
database container was removed. This was not a cloud provisioning test.

Web tests passed (93), along with its production build. Real PostgreSQL
authority integration tests covered viewer/operator permission boundaries,
one-time exchange, rotation, and revocation. The full Go suite still has a
Darwin host-metrics test failure (`top` subprocess killed); it is not a green
full-suite result. Native focused authority tests passed. The broader native
run exposed three failures: manual-profile permission fallback, the Operations
keyboard shortcut, and inconsistent scheduled-job fixture/accessibility data.
All three passed focused reruns after correction; the complete native suite
was not rerun after those fixes. CLI tests and six new management-preflight
tests passed. Management OpenTofu validation and Ansible syntax checks passed;
these are offline checks, not provider or VPN observations.
