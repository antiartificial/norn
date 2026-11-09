# Norn v3 launch history

## Read-only Mini and DO refresh (2026-11-09 14:59–15:02 UTC)

The authenticated Mini inventory at 14:59 UTC reports API health and host
status `ok` on signed V3 release
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`). It reports 30 app records, 46
services (19 passing, 27 unknown), seven queued `fleet.github.apply-dispatch`
records last updated Oct 8–10, 25 active incident groups, and four
snapshot-retention warnings. Production readiness is blocked (7 passed, 1
warning, 18 failed). The latest host assurance receipt is still the successful
operation from Sep 6; this read did not run route assurance. These queued
records and incidents remain unresolved; this read made no mutation.

Read-only `doctl --context work` reads at 15:02 UTC found all nine
`tk-staging-nyc3` Droplets active (3 control, 2 ingress, 4 app), the ingress
load balancer active, and the management-authority PostgreSQL plus application
PostgreSQL/MySQL databases online. The latest public canary probe remains the
13:09 UTC read: `/route-proof` returned `norn-fleet-canary`, and five
`/version` responses reported source
`be5ca5d969ccf3509e62ce04a833516bb5c204db` across two allocation IDs and two
node IDs. Provider activity and that earlier route proof do not establish
current DO Norn API, etcd, Nomad/Consul, or full catalog health. No provider,
deployment, route, or host mutation was run.

## Admission replay recovery model merged (2026-11-09)

Norn PR [#333](https://github.com/antiartificial/norn/pull/333) merged as
`443ca8404b8188c004d6229871b34792f0912c37`. Its TLA+ model checks durable
admission acceptance followed by a lost response, controller crash/restart
without process-local acceptance memory, and same-key replay from the durable
receipt. TLC explored 115 states and 365 steps; ten invariants and deadlock
checking passed. This strengthens offline protocol evidence only. PRs #335 and
#337 later exercised real-etcd HTTP replay across response loss and handler/
adapter reconstruction; PR #339 exercised shared-reconciler recovery across
an etcd-backed successor claim with controlled external-effect fakes. Fresh
HTTP admission with real qualification/private-input persistence and full
production executor/worker restart remain unproven; no deployment or workload
mutation was performed.

## Read-only runtime and provider refresh (2026-11-09 12:29 UTC)

The authenticated Mini inventory again returned API and host status `ok` on
source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`. It reported 30 app records,
46 services (19 passing, 27 unknown), seven queued GitHub apply-dispatch
receipts last updated Oct 8–10, 25 incident groups, and four snapshot-retention
warnings. Production readiness generated at 12:29 UTC remains blocked (7
passed, 1 warning, 18 failed). This read did not run route assurance; the
latest recorded assurance is still dated Sep 6.

Read-only `doctl --context work` queries found all nine `tk-staging-nyc3`
Droplets, both management Droplets, and the staging ingress load balancer
active. These provider reads do not establish database, Norn/API, etcd,
Nomad/Consul, workload, or public-route health. DO runtime health and Genesis
readback remain unverified. No provider mutation, deployment, or host change
was made.

## DO staging provider and canary refresh (2026-11-09 13:09 UTC)

Read-only `doctl --context work` queries found all nine staging Droplets
active (3 control, 2 ingress, 4 app), both management Droplets active, the
ingress load balancer active, and the management-authority PostgreSQL plus
application PostgreSQL/MySQL databases online. Verified public HTTPS probes
returned 200 from `/route-proof` with body `norn-fleet-canary`; five `/version`
reads all reported source `be5ca5d969ccf3509e62ce04a833516bb5c204db` across
two allocation IDs and two node IDs. `/api/health` on the canary host returned
404. This confirms provider resources and the canary application route are
serving, not Norn API health, etcd quorum, Nomad/Consul control-plane health,
catalog state, or a terminal receipt for the earlier failed deployment. No
provider mutation or deployment ran.

## Fresh release-readiness reconciliation (2026-11-09 12:25 UTC)

Norn PR [#324](https://github.com/antiartificial/norn/pull/324) merged at
`3fcdc154e247d5b32a8ad025e4fb9690390b5022`. It fixes a race where two
independent etcd clients submitted the same workload release request, both
missed the initial replay read, and the fence loser returned an error instead
of the winner's durable acceptance. The two-client integration test passed ten
consecutive runs against local etcd, a focused race-detector run, and the full
API module suite. The pre-fix five-step counterexample and fixed 14-state /
18-transition model are checked in. This proves store-level same-key
convergence only; concurrent HTTP handlers, worker/controller restart and
response loss, and production drivers remain open. No live workload release
was run.

An authenticated Mini inventory at 11:59 UTC returned API and host status
`ok`, source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`, 30 app records, 46
services (19 passing, 27 unknown), seven queued Fleet GitHub dispatch receipts,
25 incident groups, and four snapshot-retention warnings. Production
readiness is blocked (7 passed, 1 warning, 18 failed). The `latestAssurance`
receipt is still `succeeded` but dated 2026-09-06; this inventory did not run
assurance, so public-route assurance is not fresh.

At about 12:00 UTC, read-only `doctl --context work` queries found all nine
staging Droplets, both management Droplets, and the staging ingress load
balancer active. The queries did not inspect the managed databases or any Norn
API, etcd, Nomad/Consul, workload, or route state. DO runtime health remains
unverified. Fleet PR #837 at head
`eef00505d23e37c66cdaed75c5f4c5504a1cd22b` merged at 12:22 UTC as
`6335c667693d0333d4239f91edf2efb9772ab63a`. Its exact-head contract rerun
42578247221 passed after the earlier transient DigitalOcean provider v2.100.0
download failure; both Socket security checks also passed. This merged source
adds a default-off protected ingress-enrollment prerequisite. No host was
enrolled, provider mutation ran, or Genesis readback occurred. Fresh owner-
reviewed inputs and separately authorized one-use dispatch are still required.

## Release merge and gate update (2026-11-09 05:35 UTC)

Norn PR #300 (release lanes, milestones, and V3 API/client cleanup), #301 (Mini
route assurance), and #302 (offline workload release controller) are merged to
`master`; #302 is at `c792564c9440805a467a832a4fcb6b433a1f68c3`. The Mini remains
an independent node, and #301's code merge is not a fresh host readback or a
claim that production route assurance is complete. #302 is source and offline
evidence only; the operator API/CLI admission path and live workload
qualification remain open.

Fleet PRs #940 (release intent controller), #941 (fresh-Fleet offline
war-game), and #945 (provider scope contract) are merged. Stacked PR #943 closed
when #941's base merged; its credential-lifecycle changes were rebased as draft
PR #947. PR #942 moves tests to module-owned collection. Both refreshed PR
contracts failed at OpenTofu provider installation after GitHub returned 504
responses fetching the DigitalOcean provider checksum or signature. Earlier
contract phases passed. GitHub branch policy blocks merge until the required
contract passes; it was not bypassed. No provider apply, deployment, or Genesis
readback ran as part of these merges.

This file preserves the dated observations and decision trail formerly
collected in `LAUNCH-STATE.md`. Many entries are historical snapshots and
must not be read as current status. For current gates and the next actions,
see [Norn v3 launch state](LAUNCH-STATE.md). Use the relevant lane runbook for
operator procedures.

## DigitalOcean staging provider readback (2026-11-08 09:36 UTC)

Read-only DigitalOcean API queries using the configured `work` auth context
confirm that the independent DO staging testbed exists and its provider
resources report active/online. The inventory matches staging document
`antiartificial/norn-fleet@4451767624b12cbc41cc32e160a10d4b46006e0f`:

- 3 `control`, 2 `ingress`, and 4 `app` Droplets are active in `nyc3`, with
  the expected pool tags and sizes.
- `tk-staging-nyc3-ingress` is an active `nyc3` load balancer.
- `tk-staging-nyc3-application-postgres` (2 nodes) and
  `tk-staging-nyc3-application-mysql` (3 nodes) report online.
- Project `norn-staging` and VPC `tk-staging-nyc3` (`10.43.0.0/20`) are
  present.

The default `norn-pilot` doctl context is a different DigitalOcean account and
does not show this testbed; repeat future provider checks with the explicit
`work` context. A second read-only inventory at 2026-11-08 12:41 UTC again
found all nine tagged Norn staging Droplets active and the three Norn-related managed
databases online. At 13:52 UTC, a fresh provider-only inventory again found all
nine active (3 control, 2 ingress, 4 app), the ingress load balancer active,
and management-authority PostgreSQL plus application PostgreSQL/MySQL online.
These queries do not check node APIs, cluster membership, workloads, or routes;
provider status is not runtime health.

The latest protected Genesis readback remains failed at the source capability
check (run
[42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411)).
It passed runner, installed-input, and private-transport checks, then the
installed controller refused because its protected source repository did not
contain the pinned capability commit or signed target commit. The run used
Oct 21 state and route pins; do not reuse them as current evidence. Diagnose
the missing source objects, refresh state/route/host-key evidence, and bind any
new read-only intent to those refreshed inputs before dispatch.

## Fresh DO staging provider inventory (2026-11-09 01:51 UTC)

An explicit `doctl --context work` readback matched the pinned staging account
and returned nine `tk-staging-nyc3-*` Droplets, all active in `nyc3`: 3
control, 2 ingress, and 4 app. This rechecks provider VMs only; it does not
recheck load balancers or managed databases and proves nothing about Norn API,
Nomad/Consul, workloads, routes, or Genesis journal health. The protected
Genesis readback remains unpassed.

## DO staging provider recheck (2026-11-09 02:17 UTC)

The explicit `work` account again matched the pinned staging account and was
active. All nine NYC3 Droplets were active (3 control, 2 ingress, 4 app); the
`tk-staging-nyc3-ingress` load balancer was active; both managed application
databases were online (MySQL 3 nodes, PostgreSQL 2); and the staging VPC and
`norn-staging` project were present. No provider mutation ran. These reads do
not prove Norn API, Nomad/Consul, workloads, routes, or Genesis journal health;
the protected Genesis readback remains unpassed.

## DO staging provider recheck (2026-11-09 02:48 UTC)

Read-only queries with explicit `doctl --context work` confirmed the pinned
account remains active and again returned nine active NYC3 staging Droplets
(3 control, 2 ingress, 4 app), an active ingress load balancer, online
application MySQL (3 nodes) and PostgreSQL (2 nodes), and VPC
`10.43.0.0/20`. No provider mutation ran. This is provider availability only;
it does not establish Norn API, Nomad/Consul, workloads, or routes as healthy.
The latest protected Genesis readback remains failed at
[run 42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411)
because the management source repository lacked pinned Git objects. The
successful [run 42419030676](https://github.com/antiartificial/norn-fleet/actions/runs/42419030676)
is a separate release-state handoff, not a Genesis or runtime readback. Do not
describe the DO Norn runtime as online based on either provider status or that
handoff.

## Latest Mini direct release (2026-11-08 09:20 UTC)

The independent Mini advanced from signed V3 RC `bba290db041e9f497c7161543c679d9d348b742d`
(`v3.0.0-rc.1-platform-251-gbba290db`) to signed V3 RC
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`) through the direct platform-upgrade
controller. The exact target was already installed, signature-verified, and
passed passive candidate preflight before promotion. The upgrade retained
schema version 51, reader floor 5, writer floor 33, and the database identity;
it used the existing restart lane and did not claim zero interruption or
online schema migration.

Postflight evidence: `norn platform smoke` passed health, operation drain,
release marker, and event checks; `norn host status` showed Consul, Nomad, API,
host agent, supervisor, assurance timer, and cloudflared up/armed; `norn host
doctor` passed. The smoke reported `activeOps=0` for Mini-affecting work while
preserving seven queued Fleet GitHub dispatch receipts. Those records remain
queued and are not Mini work or running provider applies. A read-only inventory
at 2026-11-08 12:41 UTC again observed `/api/health=ok`, host status `ok`, and
the same exact V3 RC source SHA. It also retained the seven queued Fleet
dispatch receipts and 25 grouped open incidents. The latest host assurance
receipt predates the upgrade, so this does not claim fresh route assurance or
clear those incidents. Production readiness remains blocked and is not the
Mini direct upgrade gate. No mutation was performed during this inventory.

Node `v24.19.0` and pnpm `10.32.1` are now installed in the Mini user's NVM
version. Non-interactive maintenance still needs the NVM `bin` directory on
`PATH` and explicit `NORN_NODE_BIN`; the managed platform helper does not
automatically load NVM. No UI rebuild was needed because the exact signed
target bundle was present. The installed release is V3 RC, not a final stable
release. PR #302 remains open and was not deployed.

## Fresh Mini live check (2026-11-09 01:50 UTC)

The read-only authenticated inventory still reports `/api/health=ok` and V3
source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`). `norn host doctor` exited 0 and
passed current managed-file, LaunchAgent, runtime, and Cloudflare checks.
The `latestAssurance` receipt is still the successful operation from
2026-09-06 17:23:57 -05:00, not fresh assurance; 25 active incident groups
remain (22 critical, 3 warning). Host assurance and route reconciliation
therefore remain open. No route or host mutation was run.

The Mini API's configured Fleet document names `norn-disposable-nyc3` and
declares only 3 control + 2 ingress nodes. This is configuration held by the
independent Mini, not proof that Mini belongs to a Fleet or a readback of the
separate DO staging topology.

## Fresh Mini read-only inventory (2026-11-09 02:32 UTC)

The authenticated `norn_inventory.sh` read returned `/api/health=ok`, host
status `ok`, and V3 source
`d4dc88e0c37003a1013caed444983b2eeb50fbcb`
(`v3.0.0-rc.1-platform-252-gd4dc88e0`). It found 30 app records, 46 service
records, seven queued `fleet.github.apply-dispatch` receipts, and 25 active
incident groups (22 latest-critical, 3 latest-warning). The latest successful
host-assurance receipt remains from 2026-09-06 17:23:57 -05:00. This read did
not run host assurance or change routes; route assurance remains unverified.
The API's `fleet_configured=true` describes the separate target configuration
with `control` and `ingress` pools, not Mini Fleet membership. The raw
owner-only inventory is temporary and is not a release artifact.

## Mini event-reconcile dry-runs and route fix qualification (2026-11-09)

- At 2026-11-08 23:55 UTC, the read-only event-reconcile dry-run scanned 200
  events. It would acknowledge 54 task-restart events under the deterministic
  rule; 146 required review, including host-assurance failures, failed/lost
  allocations, and capacity warnings without later recovery evidence. No event
  was acknowledged.
- At 00:55 UTC, a second dry-run scanned 200 events: 53 matched the
  deterministic acknowledgement rule and 147 remained for review. No events
  were acknowledged. Host-assurance failures and lost allocations had no
  automatic acknowledgement rule. The seven queued Fleet dispatch receipts
  remained separate from Mini-affecting active operations.
- Norn PR [#301](https://github.com/antiartificial/norn/pull/301) adds an
  allowlisted route-forge child through the managed SOPS environment while
  retaining the runtime-mutation fence for other children. At exact head
  `fef0d6b5e2fa364c9327f733f513e5b0ec01912a`, focused shell checks passed 2
  route-fence cases and 10 recovered-baseline cases. This is offline evidence;
  the change remains undeployed and live host assurance can publish or prune
  routes. Require a fresh receipt and route inventory after the protected
  release and controlled assurance action.

## Mini direct v3 upgrade completed (2026-11-07)

The Mini has completed its controller-mediated direct upgrade as an independent
node. It is not a Fleet member. The DigitalOcean deployment remains the only
Fleet in this work and is a lower-resource staging testbed intended to resemble
production.

- The exact signed target is `bba290db041e9f497c7161543c679d9d348b742d`,
  published from protected `master` in [release run 42408397039](https://github.com/antiartificial/norn/actions/runs/42408397039)
  after full qualification run [42408083511](https://github.com/antiartificial/norn/actions/runs/42408083511).
- Current source `e117d5645f7764ac6ba6bc316aa19fa241521655` was verified as a
  signed descendant of the retained adoption source. A fresh protected backup
  and exact-target signed shadow passed before transition.
- The controller created generation
  `gen-ac83feb81071c9f0e096d9df96d53983`, transition
  `mini-v3-20261107-bba290db-r1`. Its journal is `candidate-promoted`, and its
  signed preservation receipt reports `preservationMatched=true`.
- The migration completed at schema 51 with reader floor 5 and writer floor
  33. API health is `ok`, the runtime mutation fence is inactive, and the API
  serves the exact target SHA. Platform smoke passed with zero active Mini
  operations; host doctor/status report Nomad, Consul, API, host agent and
  cloudflared up.
- Seven queued `fleet.github.apply-dispatch` receipts were preserved through
  the Mini-only drain. They are not Fleet membership or evidence of running
  provider mutations.
- A PID-reuse race interrupted host-agent fencing and required a distinct
  signed repair controller from `d4dc88e0c37003a1013caed444983b2eeb50fbcb`
  (published in [release run 42410387388](https://github.com/antiartificial/norn/actions/runs/42410387388)).
  The target candidate and its evidence remained bound to `bba290d` throughout
  repair. The regression fix is PR [#298](https://github.com/antiartificial/norn/pull/298).

This proves the Mini direct adoption and a controller-recovered schema-compatible
forward transition. It does not prove uninterrupted API availability, a
general online migration protocol, rollback across future incompatible schema
changes, production admission, or the separate DigitalOcean Fleet gates. The
older dated Mini sections below are historical snapshots and do not describe
the current runtime.

## Fresh Mini live check (2026-11-08 07:29 UTC)

Authenticated read-only inventory confirms the API still serves the exact
signed target `bba290db041e9f497c7161543c679d9d348b742d`
(`v3.0.0-rc.1-platform-251-gbba290db`). `/api/health` and host status report
`ok`; Consul, Nomad, PostgreSQL, and the workload connector report `up`. This
confirms that the Mini V3 control plane is running on the intended release.

This is not a clean whole-host assurance result. The latest host-assurance
incident is critical and reports public-route reconciliation failures for six
apps; 25 incident groups remain open (22 latest-critical, 3 latest-warning).
The newest assurance receipt returned by inventory is dated Sep 6, so the
current `host/status=ok` must not be presented as a fresh successful assurance.
The event details and route inventory are retained only in the local
mode-0700 inventory directory; no credentials were copied into this document.

Seven `fleet.github.apply-dispatch` operations remain queued from Oct 8–10.
They have not started as provider applies, but they remain live queue entries
that could be claimed; classify or safely recover them before dispatching more
Fleet work. The 25 incident groups
include older Nomad lost-allocation and capacity events, and the service
manifest still reports some services as unknown. Production readiness is
blocked (7 pass, 1 warning, 18 fail), including the compatibility development
profile, single-node quorum, ACL/TLS, local database, and recovery-readiness
gates. That production admission result is not the direct V3 upgrade gate;
route assurance and queued-operation classification remain separate
operator-readiness work.

A read-only `norn smoke platform` passed after inventory: health, app-operation
drain, release marker, and event-surface checks passed. Its `activeOps=0` uses
the Mini release drain semantics, which intentionally excludes queued
`fleet.github.apply-dispatch` operations; it is not a claim that the shared
control database has no queued operations. The smoke's critical/warning counts
are recent-event query counts, not a zero-incident assertion. A read-only
`norn host doctor` also passed its local dependency, managed-file, LaunchAgent,
and runtime checks. Neither check ran host assurance or repaired routes.

The assurance log's route failures are HTTP 409 responses stating that the
authenticated credential does not establish a stable operation actor. The
assurance LaunchAgent carries only `PATH`, while the then-installed
host-runtime invoked the managed CLI directly. Current source now routes only
`forge <app>` assurance through the SOPS-backed platform env-exec path and adds
a recovery-fence exception for the canonical host-runtime executable and its
single allowlisted `_assurance-forge` child mode. Focused tests prove the app
allowlist and fence dispatch; this source change is not yet on the Mini. A
read-only platform smoke was rerun successfully using the managed Mini binary
and SOPS. The encrypted env contains `NORN_API_TOKEN` and no `NORN_TOKEN`
override (values withheld). Smoke proves the credential works for those
read-only checks, not that the new route mutation is accepted with stable actor
context. Do not claim route assurance is repaired until a protected release is
published and a controlled assurance run produces fresh successful evidence.

Inventory artifacts were collected with the read-only `norn_inventory.sh`
procedure at 2026-11-08 07:29 UTC. The Mini remains independent of the DO
staging Fleet; its `fleet_configured` flag describes the separate target config
loaded by its control plane, not Mini membership.

## Fresh Mini inventory (2026-11-07 21:38 UTC)

Authenticated read-only inventory reconfirmed source
`bba290db041e9f497c7161543c679d9d348b742d`
(`v3.0.0-rc.1-platform-251-gbba290db`), API/host status `ok`, 30 app records
and 46 service records. Mini is not a DO Fleet member. Its API does report
`fleet_configured=true`: [`loadFleetInventory`](https://github.com/antiartificial/norn/blob/877ad4ba/v2/api/handler/fleet.go#L116)
defines that flag as a Fleet config path being set, and the loaded document is
the separate DigitalOcean staging target `norn-disposable-nyc3` in `nyc3`, with
`control` and `ingress` pools. This is
target configuration on the Mini control plane, not Mini membership or proof
of live provider state.

The API still reports seven queued `fleet.github.apply-dispatch` operations
dated 2026-10-08 through 2026-10-10. They are durable dispatch receipts, not
running provider mutations or Mini workloads; classify them before any new
dispatch. The Mini has 25 open incident groups (22 latest-critical and 3
latest-warning), and its production-readiness endpoint is blocked by the
compatibility development profile and single-node substrate. These findings
do not invalidate the successful v3 API/host postflight, but host `ok` alone
does not mean every application is healthy or that Mini is production-ready.
A read-only `norn events reconcile --dry-run` at 21:01 UTC scanned 200 recent
events: 80 matched deterministic reconciliation rules and 120 still need
review. It acknowledged nothing; the incident groups remain open until a
separate evidence-backed reconciliation.

## Post-M8 protected evidence (2026-11-07)

The protected work-staging release-state readback
([run 42345172234](https://github.com/antiartificial/norn-fleet/actions/runs/42345172234))
passed at 06:03 UTC and verified the retained signed state on all three control
nodes. A read-only application catalog readback
([run 42341905690](https://github.com/antiartificial/norn-fleet/actions/runs/42341905690))
passed at 05:27 UTC: all three controls selected the same immutable catalog and
Norn remained active. This proves readback only; no promotion or rollback
evidence is established by that run.

Grafana observability credentials passed protected lifecycle checks, but the
two latest signed rollout attempts failed on `control-01` while confirming
etcd health after service changes. The newest
[rollout run 42348418095](https://github.com/antiartificial/norn-fleet/actions/runs/42348418095)
stopped after 20 retries; its secret-safe diagnostic returned
`unclassified-etcdctl-error` (exit 1). The exact etcd failure is therefore
unknown; do not retry or continue the serial rollout until a protected,
secret-safe diagnosis identifies the cause and fresh cluster-health evidence
is available. The catalog readback above does not prove observability ingest.

The later protected DO staging Genesis readback
([run 42398114376](https://github.com/antiartificial/norn-fleet/actions/runs/42398114376))
stopped in its local runner preflight before contacting the management host:
`ansible-inventory` was absent from `PATH`. It did not change Fleet state.
Replacement Fleet PR
[#938](https://github.com/antiartificial/norn-fleet/pull/938) added the pinned
Ansible virtualenv to `GITHUB_PATH` and was squash-merged at 21:10 UTC as
`6f9b46886950f3109abe4c26eecead91f823965d` after its protected checks passed.

A fresh owner-signed read-only intent and one-use runner authorization then
dispatched protected run
[#42415121479](https://github.com/antiartificial/norn-fleet/actions/runs/42415121479).
Its runner preflight, checkout, input verification, and private management
transport passed; the runner was retired afterward. The final verifier failed
because it ran Git as root against the protected bare source repository owned
by `norn-exec`. Fleet PR [#939](https://github.com/antiartificial/norn-fleet/pull/939)
added an invocation-scoped `safe.directory` exception for that exact resolved
repository path. The change is present in the current norn-fleet checkout as
merge commit `44517676`. The post-fix readback
[#42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411)
passed protected input validation and private management/control transport,
then failed in the read-only source capability check. The installed controller
reported that the management host's protected Norn source repository lacks
the pinned capability commit or signed target commit. The log does not identify
which object is absent. The intent and run are terminal and must not be reused.
Do not retry until source-object availability is diagnosed and proven without
changing the repository, then use a fresh one-use intent. Its workflow inputs
were the original state and route files captured on Oct 21, not a fresh provider
state readback. Its private SSH probes prove connectivity to those pinned
addresses at run time; they do not prove current provider identity. The run did
not change Fleet state.

## Mini in-place upgrade checkpoint (historical observation, 2026-11-07)

PR [#289](https://github.com/antiartificial/norn/pull/289) and PR
[#290](https://github.com/antiartificial/norn/pull/290) are merged; the latter
is `e8c8ddd37f6d0db5d411ec0e3ddd7ab98114abee`, published as a signed prerelease
in [run 42358665477](https://github.com/antiartificial/norn/actions/runs/42358665477)
and imported on the Mini. The active pointer still resolves to
`3602e7e3d5fe5c0ef333b4041580d48ec15b24c5`; API health is `ok`, schema is 49,
and writer floor is 32. The candidate requires schema 51 and writer floor 33.

The prior recovered-forward transition from `3da50fe…` to `3602e7e…` has now
been finalized and verified with its exact signed controller. The original
adoption receipt was retained by digest; a successor receipt is now bound to
the running `3602e7e…` source. A fresh protected backup and exact-candidate
signed shadow for `e8c8ddd…` passed: 65 tables and 398,805 rows matched, both
migration passes reached schema 51, passive health passed, and the source was
read-only. Seven queued `fleet.github.apply-dispatch` receipts remain
preserved.

Ordinary upgrade correctly refused the writer-floor change because it cannot
prove all writers are stopped. The recovery suite exposed two generation
continuity cases: a prepared predecessor must remain supersedable without a
receipt, and an already promoted generation may need a signed-descendant
catch-up when the running source advanced later. The local fix retains old
signed journals and receipts, binds catch-up to both externally verified
release signatures and Git ancestry, and rejects unrelated or unverified
sources. Focused tests now pass: 15 transition, 22 generation preparation, and
13 resolver tests. At the time of this observation, the change was not merged
or released, and no migration or candidate promotion had occurred. See
the dated [Mini observation](m6-mini-v3-forward-upgrade-observation-2026-11-07.md)
and [online-upgrade capability proposal](online-upgrade-lessons-2026-11-07.md).

## Fleet running-upgrade checkpoint (2026-11-02 00:10 UTC)

The first protected staging canary release remains live on public HTTPS. A second protected release from source `be5ca5d969ccf3509e62ce04a833516bb5c204db` built and attested image `ghcr.io/antiartificial/norn-fleet-canary@sha256:33265daecc36215776c2029b7e0cd16463570e559c28678d3a94e249bd43ba52`, but [run 41364735627](https://github.com/antiartificial/norn/actions/runs/41364735627) failed during deployment completion. Its new Nomad job has two healthy allocations on app-03 and app-04. The previous job also has two healthy allocations, and the public `/version` route still serves the first release source `182b719bb8f5b84f8c7272e715f83553a00acb4d`. The deployment operation `6d96db37-a54e-4231-b1b8-e3e616beb3ab` is failed with manual recovery required; its durable Nomad effect says completed. The one-use runner was disabled and unregistered.

The proposed [held Fleet deployment recovery procedure](fleet-held-deployment-recovery.md) describes the checks and operator endpoint; it requires the reviewed recovery code to be deployed first.

The immediate release blocker is the route handoff for a second generation: the current initial route path rejects an already active route after creating the healthy replacement job. A fenced N+1 handoff and recovery of this failed operation are being implemented and tested. Keep both jobs for evidence and availability until a verified route switch and separate old-job retirement. Do not count the second release as deployed. The earlier replica scale and app-node drain proofs remain valid; running upgrade, rollback, loaded node-count contraction, HA fault/restore, soak, and production promotion remain open. No reliable production release date is established.

## Fleet staging canary checkpoint (2026-11-01 23:49 UTC)

The first protected Fleet canary release completed in [run 41359827250](https://github.com/antiartificial/norn/actions/runs/41359827250)
from source `182b719bb8f5b84f8c7272e715f83553a00acb4d`. Norn accepted the
signed, attested, content-addressed image
`ghcr.io/antiartificial/norn-fleet-canary@sha256:1321f05a19f2e3af0bd3bd2df4b6c2c0878d0d38d0866a3d98f7f9f15b321f63`.
Deployment `1584a4b1-03c8-43be-86ef-b00b943431a4` and operation
`ff94b712-d5d6-4933-9b19-2a228db98c9f` finished successfully. Nomad
reported two healthy, running `web` allocations on distinct ready app-pool
nodes `app-01` and `app-02`. Public HTTPS returned 200 for `/route-proof` with
the signed body digest
`75790fc8d9898d5a12787ac491b6f471619cde0efc851be9f26db608b6fbb37c`
and for `/version` with the released source SHA; TLS validation passed. The
one-use private runner stopped, consumed its authorization, and unregistered.

Norn then accepted an explicit `web` scale from two to three
(operation `2ae52865-3834-49bb-9f56-6c7bc9b3f03b`) and back to two
(operation `4e9b6a39-e8a9-4f5e-a0e2-5825f8e34cc3`). Both operations
finished `succeeded`. At three, the scale-status API reported three desired,
running and healthy allocations on distinct ready app nodes `app-01`,
`app-02` and `app-04`; 20 public `/version` requests reached all three
allocation IDs with the released source SHA. After downscale, scale-status
reported two desired, running and healthy allocations, and 12 public requests
reached both remaining allocation IDs. The public `/route-proof` body still
matched the signed digest during the three-replica phase.

A bounded app-node drain exercise then moved the live `web` allocation off
`app-01` to `app-03`, and a second drain moved the `app-02` allocation to
`app-04`. Each drain was disabled afterward; all four app nodes returned to
ready with no drain strategy. During the second transition, 180 consecutive
public HTTPS `/version` requests succeeded through `curl` certificate
validation and returned the released source SHA. Requests reached the old
`app-02` allocation and both replacement allocations. Final Norn scale-status
reported desired, placed, running and healthy counts of two. The public
`/route-proof` body still matched the signed digest. This proves scheduler
replacement and sampled public continuity during sequential drains; it does
not prove node retirement, fault recovery, a sustained load budget or Norn's
Fleet generation executor.

This is a staging release and live two-host workload proof. It advances
replica-scale evidence only; it does not prove fresh-Fleet provisioning,
platform upgrade, or mixed application-version rollout. The live 2→3→2
**replica** scale cycle and bounded app-node
drain are proven, but loaded node-count 2→3→2 replacement, worker drain,
leader failover during
route mutation, running upgrade, rollback, whole-node restore, soak, and
production promotion remain unproved. The ingress publishers currently name
the elected control03 address `10.43.0.2` for route authority. An API leader
change requires authority reconciliation before another route publication;
the current successful route remains served. The Mini transition described
below is independent of this Fleet checkpoint.

## Mini adoption evidence checkpoint (historical, 2026-11-01 05:33 UTC)

Pinned-address SSH and authenticated loopback inventory verify that the Mini has
completed its guarded PostgreSQL v2-to-v3 adoption as an independent
development Norn. The active API reports exact source
`3602e7e3d5fe5c0ef333b4041580d48ec15b24c5` and version
`v3.0.0-rc.1-platform-34-g3602e7e3`; `~/norn/current` resolves to that immutable
release. The original `com.norn.api` LaunchAgent is running through the managed
launcher in active/check mode. Health is `ok`, the runtime mutation fence is
inactive, and Consul, Nomad, PostgreSQL, S3 and SOPS report up. The selected
recovered-forward generation remains recorded as `candidate-promoted`, with its
preservation receipt retained. Current inventory found 30 app records, 46
services and host status `ok`.

This confirms the Mini live-adoption portion of M9 and provides transition
evidence for M5; M5's copied-environment rehearsal gate remains a separate
assessment. It does not qualify a repeat without operator
intervention, a current-`master` v3-to-v3 refresh, production admission, Fleet
etcd or HA, or full M9 adoption. The completed transition required bounded
operator intervention to correct the owner-only release-key path, fill one
exact preservation digest under the promotion lock, and temporarily accommodate
host-agent startup timing; those facts remain part of the evidence rather than
an autonomous-repeat claim. The Mini checkout and current `master` may advance
independently of the immutable runtime source, so neither is runtime provenance.

The DigitalOcean staging Fleet is the only Fleet in this work: a separate
testbed intended to resemble production with lower resource use. Mini is not a
member. The Mini API's configured DO target and queued Fleet dispatch receipts
are control-plane records, not evidence that Mini is a Fleet node or that
provider work is running. The DO testbed retains its own gates for directly
deployed representative workloads and data, placement
and loaded node-count 2-to-3-to-2 scaling with safe drain, running-version upgrade under
traffic, whole-node fault/restore and HA, external admission, soak, and
operational handoff. Classify the seven queued dispatch receipts before any
new Fleet dispatch.
Production readiness is still blocked. No Mini transition should be replayed or
made dependent on the separate Fleet work.

## Operating lanes and API retirement boundary (historical pre-upgrade snapshot, 2026-11-07)

The release has distinct execution lanes across independent environments:
(1) Direct Mini for its independent, individually controlled node, (2) Fleet
bootstrap for the DigitalOcean staging testbed with the default two
application node pools, (3) Fleet upgrade for an existing Fleet with workloads,
and (4) capacity changes as a separate, workload-aware operation. The direct
Mini upgrade did not require Fleet signing authority or Fleet connectivity,
and still used a verified signed release, protected backup and shadow evidence,
controller-mediated fencing, health checks, and a recoverable journal. Mini's
control API may hold configuration for the separate DO staging target; that
does not make Mini a Fleet member. Fleet bootstrap is not the Fleet
running-upgrade gate; online rollout should preserve availability where proven,
with a controlled offline window as an explicit fallback.

The Mini's adoption and post-upgrade verification are now complete. A source
review found no `/api/v2` route namespace: the production router exposes the
current `/api/v1` contract and older unversioned `/api` handlers. The `v2/api`
Go import path is the repository's package layout, not evidence of an HTTP API
version. A specific Fleet offline-effect subprotocol is still explicitly
versioned in the route `/api/v1/fleet/offline/attempts/{planID}/{attemptID}/effect-grants/v2`;
its tests protect one-use evidence and recovery behavior. It needs a caller and
protocol migration review before removal as part of V3-only support. Tests
mentioning “legacy”, “V2”, or “Postgres” therefore cannot be removed by name
alone; many protect durable state recovery, signed Fleet protocol revisions,
or Postgres conformance. Before retiring any old
unversioned handler, trace supported callers and replace its behavioral
coverage at the current contract boundary. Keep migration/recovery and
rollback-read compatibility coverage until the corresponding stored state is
no longer supported. No test deletion is justified by the route/name search
alone.

This is a correction to the pre-upgrade terminology in this historical
snapshot. PR [#295](https://github.com/antiartificial/norn/pull/295) merged the
Mini generation catch-up changes as `3f51e35ad246c68a835977d1f1e3b8fdf0a8511c`.
The signed prerelease for `0701add87b34d0de7ead5031579b3d74cd829bd7` was
published in [run 42406296055](https://github.com/antiartificial/norn/actions/runs/42406296055).
The generation catch-up logic preserves older signed journals/receipts and
binds a completed generation to the exact current source through external
signature verification and Git ancestry; it does not authorize migration or
candidate promotion by itself.

Fresh authenticated Mini inventory on 2026-11-07 reports source
`e117d5645f7764ac6ba6bc316aa19fa241521655`, version
`v3.0.0-rc.1-platform-248-ge117d564`, API health `ok`, and
`runtimeMutationFence=inactive`. `~/norn/current` resolves to that source. The
active recovered-forward generation is `gen-1d647aa65bac5b2b5cdda675fdb8cb85`,
transition `mini-v3-20261107-38fcec83`, candidate
`38fcec83380c7ba989d46e11db6cc449510fad86`; its journal says
`candidate-promoted`. Git confirms that candidate is an ancestor of the running
source. This confirms a healthy V3 runtime and an earlier promoted candidate;
it does not prove the pending e117-based schema migration. The signed
`0701add8…` release was published, but Mini preflight stopped before candidate
preparation because its legacy adoption guard accepts only the original
adoption source or exact journaled candidate. A
follow-up fix allows evidence preparation and recovered-forward control only
after both source releases verify externally and Git proves ancestry; ordinary
upgrade remains gated on the completed recovered-forward journal. That fix is
still unreleased. No candidate import, database migration, or candidate
promotion was verified in this checkpoint.

This checkpoint supersedes older present-tense Mini source, schema, and
generation statements below. Preserve those sections as historical evidence.

## Track boundaries (2026-10-11)

The v3 launch has two independent environments. **Mini is an independent,
single-node Norn installation and is not part of a Fleet.** It retains its
unmanaged PostgreSQL, and its startup and upgrade do not require Fleet
connectivity, Fleet etcd, or Fleet management. Separately, the only Fleet in
this work is the DigitalOcean staging testbed, intended to resemble production
with lower resource use. Its bootstrap and upgrade qualification are distinct
Fleet gates, not Mini prerequisites.

The Mini track is: publish and verify the exact signed platform release, take
and verify a fresh Mini-local recovery copy, run the protected Mini
transition/upgrade, then verify API version,
health, database access, preserved applications, and host assurance. Fleet
deployment, etcd quorum, ingress, workload/scale testing, and teardown remain
separate Fleet acceptance milestones; none is a prerequisite for upgrading the
Mini.

As of 2026-10-11, the Mini drain corrections in PRs
[#137](https://github.com/antiartificial/norn/pull/137) and
[#138](https://github.com/antiartificial/norn/pull/138) are merged. The signed
rehearsal correction in PR
[#140](https://github.com/antiartificial/norn/pull/140) is also merged as
`a059f2e5b42e8df30c3fe4460cbd6455a0ea4722`. Its immutable signed prerelease is
[`platform-a059f2e5b42e8df30c3fe4460cbd6455a0ea4722`](https://github.com/antiartificial/norn/releases/tag/platform-a059f2e5b42e8df30c3fe4460cbd6455a0ea4722).
The Mini imported and signature-verified that exact release. A fresh Mini-local
backup and a signed private-database/LaunchAgent shadow rehearsal passed. The
live upgrade then stopped before migration or promotion: the candidate raises
the PostgreSQL writer floor from 31 to 32, and the ordinary restart lane cannot
prove that every old writer is fenced. A prior transition is recorded as
`candidate-postflight-failed`; its signed adoption receipt supports forward
work but does not authorize replay of the old one-way procedure or prove live
writer quiescence. Mini remains on its prior signed release and schema 47.
The next Mini-specific engineering gate is a separately journaled,
interruption-tested recovered-forward transition that proves all writers are
quiesced before migration and only moves forward. Fleet deployment, Fleet
management nodes, and Fleet etcd are not prerequisites for this Mini update.

Historical architecture note: Mini is the independent single-node development
platform and uses its existing unmanaged PostgreSQL service. It does not host
Fleet nodes or require Fleet management nodes or an etcd quorum. Earlier
proposals considered making Mini a Fleet member; that is not the current
topology. The DigitalOcean Fleet is a separate staging testbed. The Mini's
current API target configuration for DO is recorded separately above.

At 2026-10-11 19:42 UTC, protected signed release
`798c82847c04156d99a9876c0bb0d6d4b76f901b` was published and independently
verified; it is a v3.0.0-rc.1 prerelease, not a stable v3.0.0 release. Direct
SSH and loopback API checks reconfirmed Mini still serves
`26147c39a554b73b6761a371edee5a9591c81d3f` (`v2.20.0-platform-72-g26147c39`),
schema 47/writer floor 31, health `ok`, and single-node PostgreSQL/Consul/Nomad.
No candidate was promoted.

The failed transition evidence binds historical candidate
`e7301f779041cd22f1872090323d3b8ed5c4611b`, while the current signed runtime is
`26147c39a554b73b6761a371edee5a9591c81d3f`. Its reconciliation records
`matchesBefore=false`; that is an explicit loss of proof for the old baseline,
not evidence that current data should be discarded. The local correction now
models these as separate identities (`sourceSHA` and `incidentCandidateSHA`),
retains the v1 receipt and all historical files, and captures a fresh current
Mini workload digest for the new forward transition. It still requires a fresh
verified database backup and exact-candidate signed shadow restore before any
live migration. This correction is not yet merged or released, and no live
Mini state has been changed.

Observed 2026-10-11. This is the single current handoff for the
pragmatic deployment objective. The [milestone contract](execution-milestones.md)
defines the exit criteria; percentages below are engineering estimates, not
owner sign-offs. Recheck external state before any new mutation.

## Latest Mini transition checkpoint (2026-10-11 21:00 UTC)

The Mini is being upgraded as its own single-node Norn instance. It uses its
existing unmanaged PostgreSQL and does not need Fleet nodes, Fleet management
nodes, or Fleet etcd. Fleet availability is not an upgrade prerequisite. Mini
is not a Fleet member; its control API's configured DO staging target is
separate from the Mini host and its workload topology.

Norn PR [#144](https://github.com/antiartificial/norn/pull/144) merged as
`85fb2830ac18a711b2e3e20de45c1adcbbd4935d`; the protected signed prerelease
[`platform-85fb2830ac18a711b2e3e20de45c1adcbbd4935d`](https://github.com/antiartificial/norn/releases/tag/platform-85fb2830ac18a711b2e3e20de45c1adcbbd4935d)
was published for that exact SHA. The Mini imported and verified it. A fresh
local backup and exact-candidate shadow rehearsal passed. The live forward
transition `mini-v3-20261011-85fb2830` is paused in `legacy-fenced`: both
`com.norn.api` and `com.norn.host-agent` are fenced with exit 78, `current`
still points to `26147c39a554b73b6761a371edee5a9591c81d3f`, and PostgreSQL
remains at schema 47. No v3 migration or candidate promotion has occurred.

The immediate gate is seven queued `fleet.github.apply-dispatch` receipts for
pilot `pilot261006c`. Their bound GitHub apply runs are terminal (five failed,
two cancelled); the `Apply verified plan` step is skipped for each. The
migration quiescence contract currently blocks on every nonterminal operation,
while Mini's own preflight explicitly preserves these queued Fleet dispatch
receipts because they do not mutate Mini. The fix is to align those contracts
narrowly: allow only queued `fleet.github.apply-dispatch` receipts to remain
unchanged through migration, while continuing to block every other active
operation and any additional database client. Do not edit operation rows
directly, bypass the guard, or alter the journal's candidate binding. Then
resume the same signed transition through a supported guarded candidate
retarget path, if its journal and schema checks still validate. Mini's
independent upgrade must not be held for provisioning or starting a Fleet
cluster.

## Current decision

**Norn v3 is not deployed on the DigitalOcean staging Fleet** (status at this
historical checkpoint). Mini's single-node upgrade is independent of Fleet
deployment. Mini was fenced during its single-node upgrade at that time;
it is not serving its normal API until the recovered-forward transition
completes. Its development profile and preserved applications do not qualify
Fleet or complete the release. A Fleet attempt remains a separate acceptance run and
requires fresh credentials, a repaired partial-apply/recovery lane, and a
complete workload and failure proof. Do not reuse `pilot261004b` state, runner,
keys or approvals.

At 21:29 UTC, Norn PR [#123](https://github.com/antiartificial/norn/pull/123)
merged to protected `master` as `1ca28934034c48f28aed4b97b6d4971d69b642e3`.
Norn CI [37376169196](https://github.com/antiartificial/norn/actions/runs/37376169196),
Repository CI [37376169212](https://github.com/antiartificial/norn/actions/runs/37376169212),
and docs deployment [37376169180](https://github.com/antiartificial/norn/actions/runs/37376169180)
all succeeded on that exact SHA. The Norn observer/status controller is now in
protected master; this is not a published platform release or live Fleet
deployment.

At 21:34 UTC, Fleet PR [#258](https://github.com/antiartificial/norn-fleet/pull/258)
merged as `e4756190670aa3a8afa6d3cfdc6344a531dd4376`. The staging doctor now
accepts the verified GitHub squash-commit identity. It still reports three
missing input groups. `staging-plan` is missing `ADMIN_CIDRS_JSON`,
`DIGITALOCEAN_TOKEN`, and `STATE_ACCESS_KEY_ID`, `STATE_BUCKET`,
`STATE_ENDPOINT`, `STATE_REGION`, and `STATE_SECRET_ACCESS_KEY`. `staging` is
missing `DIGITALOCEAN_TOKEN`, `NORN_API_URL`, and the same five state-store
values. The protected runner configuration is missing
`NORN_FLEET_BOOTSTRAP_VARS_FILE`, `NORN_FLEET_KNOWN_HOSTS_FILE`,
`NORN_FLEET_PRIVATE_INTERFACE`, `NORN_FLEET_SSH_PRIVATE_KEY_FILE`, and
`NORN_FLEET_SSH_USER`. No provider operation has been run from staging.

At 00:10 UTC, the protected `platform-release` environment secret
`NORN_RELEASE_IMMUTABILITY_CHECK_TOKEN` was updated after the release-check PAT
was regenerated. At 00:12 UTC, workflow run
[37392793546](https://github.com/antiartificial/norn/actions/runs/37392793546)
was dispatched for exact merged SHA
`1ca28934034c48f28aed4b97b6d4971d69b642e3`. Authorization, pinned UI build,
and all four Linux/macOS amd64/arm64 bundle jobs passed. The `publish` job is
waiting for review in the protected `platform-release` environment; no
immutable release has been published yet.

During browser inspection, the newly generated release-check PAT value appeared
in an accessibility snapshot. Treat it as exposed and revoke/replace it before
approving publication. The environment secret must then be refreshed with the
replacement. The running publish job may fail if it reaches the immutability
check after revocation; rerun the same exact SHA after rotation if needed. No
token value is recorded here.

The current staging doctor still reports missing staging-plan secrets
`ADMIN_CIDRS_JSON`, `DIGITALOCEAN_TOKEN`, `STATE_ACCESS_KEY_ID`, `STATE_BUCKET`,
`STATE_ENDPOINT`, `STATE_REGION`, and `STATE_SECRET_ACCESS_KEY`; staging is
missing `DIGITALOCEAN_TOKEN`, `NORN_API_URL`, and the five `STATE_*` values.
Protected runner variables remain missing:
`NORN_FLEET_BOOTSTRAP_VARS_FILE`, `NORN_FLEET_KNOWN_HOSTS_FILE`,
`NORN_FLEET_PRIVATE_INTERFACE`, `NORN_FLEET_SSH_PRIVATE_KEY_FILE`, and
`NORN_FLEET_SSH_USER`. No provider operation has been run from staging.

At 00:22 UTC, the newest authenticated Mini inventory reports source
`26147c39a554b73b6761a371edee5a9591c81d3f`
(`v2.20.0-platform-72-g26147c39`), host `ok`, Fleet unconfigured with zero
pools, and production readiness blocked (6 pass, 19 fail, 1 warning). It found
28 apps, 45 services, zero active operations, and 25 active incidents. Evidence
is at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261006T002155Z`.
The merged Norn SHA has not been released or promoted to Mini.

At 19:34 UTC, a fresh authenticated Mini inventory again reported host status
`ok`, but `fleet_configured=false`, zero node pools, and production readiness
`blocked`. It found 28 apps, 45 services, zero active operations, and 25 active
incidents. The detailed safe inventory is at
`/tmp/norn-inventory-20261005T1934Z`; this confirms the Mini is online on its
v3 API source but does not establish Fleet deployment readiness.

At 20:01 UTC, a new authenticated read-only Mini inventory again reported
source commit `26147c39a554b73b6761a371edee5a9591c81d3f`
(`v2.20.0-platform-72-g26147c39`), host status `ok`, Fleet
`configured=false` with zero node pools, and production readiness `blocked`.
It observed 28 app entries, 45 service entries, zero active operations, and
25 incidents. Local observation files are at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T200109Z`.
This confirms the Mini v3 API is online; it does not show a Fleet deployment.

At 20:30 UTC, a fresh authenticated read-only inventory remained unchanged:
source `26147c39a554b73b6761a371edee5a9591c81d3f`, host `ok`, Fleet
unconfigured with zero pools, and production readiness blocked (6 pass, 19
fail, 1 warn). It reported 28 app entries, 45 service entries, zero active
operations, and 25 incidents. Evidence is at
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T203015Z`.

At 20:11 UTC, the next authenticated read-only Mini inventory was unchanged:
source `26147c39a554b73b6761a371edee5a9591c81d3f`, host `ok`, Fleet
unconfigured with zero pools, and production readiness blocked (6 pass, 19
fail, 1 warn). It again reported 28 app entries, 45 services, zero active
operations, and 25 active incidents. Evidence is in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T201102Z`.

At 21:01 UTC, focused local tests for the separate disposable-retirement
controller worktree passed (78 tests across the coordinator, delegation,
deadline proposal, root retirement, backend cleanup, and manifest builder).
These use injected test adapters and fixtures. The worktree still contains
uncommitted changes based on Fleet `main` `5210307`; the controller has no real
executor/Spaces adapters, fresh deadline-time fence approval producer or
renewal, or restart supervisor. Its runbook still classifies deadline
retirement as not implemented and unrehearsed. This work was independent of the
Norn observer-controller integration and remains a prerequisite to safely
running another billable disposable Fleet pilot.

At 17:42 UTC, Fleet PR #256 merged to `main` as `5210307`. Merged-main plan
`37350374522` and contract validation `37350374566` both succeeded. The change
binds resumable paired backend cleanup to the exact locked run and adds a
validation-only deadline descriptor checker. It does not implement or qualify
an unattended deadline controller. Fleet PR #255 remains merged at `abb0b00`;
its merged-main validation (`37336272528`) and planning (`37336272298`) passed.
Preceding commit `1fcaeb7` passed
contract validation run `37343349906`, including the full Python unittest
suite, local retirement qualification, workflow validation and OpenTofu checks.
Commit `abaabc4` clarifies retirement CLI help text; validation run
`37344577776` passed the same contract suite at 17:01 UTC. Commit `d4eee3e`
adds a regression test proving an already-started backend cleanup can resume
after its short approval expires, while a new cleanup cannot start after expiry.
Full validation run `37346380888` passed at 17:15:58 UTC, including the full
Python unittest suite, local retirement qualification, workflow checks,
OpenTofu validation and disposable pilot validation. Review then found a race
where a replaced cleanup scope could select another run after the shared lock
was acquired. Commit `3f41503` binds the reopened scope and canonical ledger path
to the locked run; its focused regression test and full validation run
`37347927438` passed. PR #256 also
corrects the disposable README's stale `pilot261004b`
label to the actual `pilot261005a` budget-gate ID, adds a non-executable
descriptor checker, and makes an already-authorized backend cleanup retryable
after its approval expires. It records deadline-controller design requirements
but does not provide a scheduler or make a pilot safe to start. The Fleet
account has no
matching `pilot261005a` or `pilot261005c` droplets, databases, load balancers,
projects, VPCs, volumes, firewalls, reserved IPs, snapshots, or runner labels at
16:20 UTC; this is still not Spaces bucket-zero proof. Norn launch-state PR #120
was a documentation-only handoff and merged to protected `master` at 17:31 UTC
as `d67b8bf01346ef8e54f25e1119c7ffe532e05e1d`. Its `Repository CI` run
`37348527855` and `Norn CI` run `37348527835` both passed, including the required
API/CLI, Fleet pilot workload, OpenAPI, workflow lint, Web UI, M4 durable replica,
and release-bundle checks. These workflows had been manually disabled; both are
now active. This verifies the current Norn branch gates, not a Fleet deployment.
Fleet issuer PR #210 is conflicting
with main; lifecycle PR #175 has a failed contract check. Neither is a current
deployment gate result.

The approved `pilot261004b` window was $20 maximum, four hours active plus one
hour for teardown, ending **2026-10-05 08:30 UTC**. The protected
[apply run 37273763546](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
at Fleet `main` `ca52148d69eecd70a843938b72c2bc882a066c5d` stopped during provider
creation at 06:50:30 UTC with exit 143. The runner was online and idle after
termination; the signal sender is unproven. Norn attempt
`eb349686-c1ac-439d-b7b6-cf1fc2443d9f` was later marked abandoned after its
heartbeat expired. The remote Fleet state persisted at serial 3, lineage
`ffe289b7-a711-4ca6-b84d-7887c67f4d5c`, with five droplets, load balancer,
VPC, firewalls, project and tags. It did **not** contain either managed database,
although DigitalOcean had created both. No bootstrap, public ingress, etcd
quorum, application workload or scale proof followed. Automatic
[recovery run 37274493490](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490)
failed at source-run name validation; the repository recovery inventory would
also hold on the two provider-only databases. Plain apply replay was unsafe.

At 13:47 UTC, well after the teardown deadline, a fresh DigitalOcean inventory
still showed seven pilot droplets, three managed databases, the Fleet load
balancer, four firewalls, two VPCs and two projects. This was an **overrun
incident**. Exact-ID emergency deletion began after preserving private state
and provider evidence. By 13:58 UTC, fresh DigitalOcean list calls showed zero
`pilot261004b` droplets, databases, load balancers, firewalls, VPCs, projects,
tags, volumes, snapshots, reserved IPs and SSH keys. Both pilot Spaces state
buckets, every retained object version and both scoped Spaces keys were removed;
a new full-access Spaces inventory key found zero pilot buckets and was revoked.
The exact GitHub pilot runner and the staging secrets/variables created for
this pilot were removed. DNS A lookup for
`pilot-pilot261004b.betabung.com` returned no address. These are technical
absence observations after **emergency unprotected cleanup**, not a signed
protected final-zero receipt. The final pilot-specific DigitalOcean bill is not
yet available. The account's month-to-date balance cannot be attributed to
this pilot, so compliance with the $20 cap remains unproven.

## Controller and executor state

There are three distinct controller/executor workstreams; none alone proves
an autonomous bounded pilot or a live deployment:

* The Norn Fleet observer/controller implementation began on
  `feature/v3-fleet-controller` over the weekend and was integrated by PR #123
  into protected master on Oct 5. It is merged, tested in post-merge CI, and
  present in source; it derives and stores Fleet target, observation and status.
  It does not provision provider infrastructure. Release publication, Mini
  promotion and Fleet deployment remain separate gates.
* Fleet executor PR [#257](https://github.com/antiartificial/norn-fleet/pull/257)
  remains a draft on `feature/fleet-controller-executor` at `64cb7b0`, based on
  Fleet main `5210307`; its contract check passed. This is separate newer work,
  not the weekend Norn observer/controller integration. The PR explicitly
  supports only the legacy PostgreSQL no-drain lifecycle, refuses v3/etcd before
  provider calls, and does not ship the Norn executor API route. It is not a v3
  deployment path and must not be installed on a v3/etcd staging root. No
  provider-backed E2E was run.
* The checkout `norn-fleet-deadline-retirement` is still on merged Fleet
  `main` commit `5210307` with uncommitted edits, not on an independent commit
  branch. Those edits add delegated owner authority/journaling at existing
  root/backend retirement boundaries and a post-create root approval producer.
  New `scripts/disposable_retirement_controller.py` provides a local-only
  persisted coordinator with injected stage adapters, separate controller
  locking, HMAC/hash-chained intents, proof checks, restart recovery hooks, and
  T0+5 breach-and-continue behavior. The full Fleet Python unittest suite
  passed at 20:06 UTC after the latest coordinator proof-validation changes:
  1,643 tests, four skipped, in 258.317 seconds. The earlier coordinator run
  had 1,641 tests, four skipped, in 278.771 seconds. This suite includes
  existing executor and fixture tests; it does not qualify a live provider
  adapter, unattended restartable orchestration, or deployment. Subsequent
  local changes now enforce the full root-zero and
  backend-cleanup receipt field sets, digest shapes, delegated run/root scope,
  independent provider-observation hashes, bucket bindings, and prior-stage
  receipt digests; 11 focused tests, `py_compile` and `git diff --check` pass.
  This is structural binding only: the coordinator still does not reopen and
  validate retained state exports, executor ledgers, or external provider
  evidence. The existing per-stage CLI executors do not yet have an adapter or
  launchd supervisor. The complete
  1,641 tests, four skipped, in 278.771 seconds on 2026-10-05. Both Fleet and
  launch-state `git diff --check` pass. It has no real
  root/backend command, approval, fence-renewal, or Spaces adapter and no CLI or
  launchd supervisor, and has not passed sleep/reboot/partial-apply rehearsal.

The three workstreams cover separate boundaries (Norn desired-state
observation, executor-host execution, and disposable-pilot retirement). They
need an explicit integration/release path; do not infer they compose safely
because their local tests pass.

After merged Fleet PR #256, a separate local Fleet checkout
`norn-fleet-deadline-retirement` on branch
`codex/executable-deadline-controller` contains new, unmerged work. The
`scripts/disposable_retirement_delegation.py` module validates an owner-HMAC
run delegation and maintains an owner-only hash-chained retirement journal.
The journal records and fsyncs a T0+5 breach marker before later cleanup can
continue. Both the root executor and paired-backend `apply` now have opt-in
delegation checks at their existing shared-lock mutation boundaries, with
account, budget, activation, source, evidence, lock, state/root, provider-scope,
and ordering bindings. New `disposable_deadline_approval.py` and manifest
builder support derive a short-lived HMAC approval from the owner delegation
and fresh post-create root output, state, provider scope, retention, fence
approval digest, and MySQL receipt. The root executor verifies that approval
against the exact signed delegation. Focused tests after these edits pass: 8
manifest-builder, 26 root-retirement, 19 backend-cleanup, 11 delegation/journal,
  and two deadline-proposal tests. The earlier Python suite passed 1,632 tests
  with four skipped in 300.372 seconds at 19:14 UTC, before the coordinator
  module was added. The subsequent full run including the coordinator also
  passed: 1,641 tests with four skipped in 278.771 seconds. A refreshed Mini
  inventory at 19:34 UTC again reports Fleet unconfigured and production
  readiness blocked; the controller suite pass does not alter the deployment
  gate.
  The deadline
proposal validator accepts the complete delegation CLI
bundle but remains validation-only. There is still no fully wired end-to-end
controller, fresh fence-approval producer/renewal, restart supervisor, or
sleep/reboot/partial-apply rehearsal. Do not use this branch to authorize or
start a billable pilot.

The two offline `pilot261004b` Tailscale management records (100.120.209.89
and 100.122.147.29) were removed after action-time user confirmation; the
console then showed 14 machines and no `pilot261004b` machines. The five
one-use Fleet auth keys are invalidated. The broad OAuth client that issued
them was previously revoked; a replacement scoped client was requested but
not generated. The `pilot261004b` DigitalOcean API token was revoked through
DigitalOcean’s token revocation endpoint, and a follow-up account request
returned HTTP 401. Its local token file was removed after a hash-only receipt
was saved. Technical external cleanup is now observed, but the final pilot
bill and a signed protected final-zero receipt remain unavailable.

**Reusable DigitalOcean capability:** The authenticated `doctl` context and
DigitalOcean API can eliminate repeated dashboard sign-ins for supported
account, resource, and token-management operations. An already-issued pilot
personal access token can revoke itself through DigitalOcean’s API, so cleanup
of that token does not require a second dashboard sign-in when its local token
file is still available. For `pilot261004b`, `POST` to DigitalOcean’s
[OAuth revocation endpoint](https://docs.digitalocean.com/reference/api/oauth/)
with that token as both Bearer authorization and the `token` form parameter
returned HTTP 200; a subsequent `/v2/account` call with the same token returned
401. This can eliminate a dashboard sign-in specifically for revoking an
existing token; it does not create a token, restore expired access, or prove
provider resources are gone. Record only a hash and response statuses, then
remove the local file. DigitalOcean documents the endpoint for OAuth tokens;
support for a dashboard-issued personal access token is a live observation to
recheck before relying on it in another pilot.

At 17:12 UTC, the signed-in local `doctl` default context authenticated
successfully (a fresh `doctl account get`) to
the intended `theartificial@hotmail.com` account (UUID
`05fe610a42b6d50c65344dd87bd842ce35ab036d`). Supported DigitalOcean API
inspection and operations can use this existing CLI session without a new
dashboard sign-in, including later token revocation when the active CLI context
has the required account authority. It does not supply fresh run-scoped
credentials or approval, and `doctl` cannot enumerate Spaces buckets in this
workflow; retain the separate Spaces API/S3 inventory for that proof. Read-only CLI checks found no
`pilot261005a` name matches among droplets, databases, load balancers, projects,
or VPCs. At 17:44 UTC, corrected read-only `doctl` name checks also found no
`pilot261004b`, `pilot261005a`, or `pilot261005c` matches among droplets,
databases, load balancers, firewalls, VPCs, or projects. These name checks are
not complete provider-zero evidence and did not inspect Spaces buckets. A
fresh read-only check at 18:14 UTC again authenticated to the intended account
and found no `pilot261005a` or `pilot261005c` matches in droplets, databases,
load balancers, firewalls, VPCs, or projects. This is still only a name-collision
check; volumes, snapshots, reserved IPs, SSH keys, project resources, DNS, and
Spaces buckets were not checked in this observation. No provider mutation was
made. A
plain Mini `norn fleet pools` CLI request at 17:45 returned HTTP 401
`authenticated_principal_required`; the documented owner-host SOPS inventory
path then authenticated successfully at 17:50. It confirmed Fleet
`configured=false`, zero node pools and zero plans. No Fleet plan or provider
mutation was made in this turn.

**Operational follow-up:** Use the authenticated `doctl` context first for
supported DigitalOcean account, resource, and token operations. This avoids
re-entering the dashboard for routine API-capable tasks and may allow a known
active token to be revoked through the API during cleanup. Use the dashboard
only for actions that require interactive UI authentication or are not exposed
by the API. Before a future pilot, verify the selected context/account and
required scopes with a read-only request; this convenience does not replace a
fresh run-scoped credential, bounded approval, or a separate Spaces inventory.

## Mini and source

On 2026-10-05 at 17:50 UTC, a fresh authenticated owner-host inventory showed
the Mini loopback API healthy on source `26147c39a554b73b6761a371edee5a9591c81d3f`,
displayed as `v2.20.0-platform-72-g26147c39`. The platform version label's
source includes v3 work. Authenticated production-readiness returned
**blocked**: 6 of 26 checks
passed, 19 failed, and 1 warned. Current failures include the development
profile and compatibility auth, single-member Nomad/Consul, disabled ACLs and
incomplete TLS, local control database without PITR/replica, application
deployment provenance, snapshot/restore posture, and missing recovery drills.
The host status is `ok`; this does not override production readiness.

At 19:15 UTC, authenticated owner-host inventory was repeated using the
Norn-platform inventory script. It again reported healthy Mini API source
`26147c39a554b73b6761a371edee5a9591c81d3f` (`v2.20.0-platform-72-g26147c39`),
host status `ok`, Fleet `configured=false`, zero node pools, zero plans, and
production readiness **blocked** with 6 pass, 19 fail, and 1 warn. It observed
28 app entries, 45 service entries (18 passing, 27 unknown), zero active
operations, and 25 active incidents. Snapshot-retention warnings remain for
field-harbor and turnkey-offer-intake; mail-mcp and signal-sideband still need
secret attention. This read-only inventory made no provider or control-plane
mutation. The local observation files are in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T191507Z`.

At 20:38 UTC, a new authenticated owner-host inventory again found source
`26147c39a554b73b6761a371edee5a9591c81d3f`, host status `ok`, Fleet
`configured=false` with zero pools, zero active operations, 25 active incidents,
and production readiness **blocked** at 6 pass, 19 fail, and 1 warn. It listed
28 app entries and 45 services. The snapshot is in
`/var/folders/c8/d4bdjwzn2qgfmg3hqz7qj7ch0000gn/T/norn-inventory-20261005T203815Z`.

The authenticated `doctl` context still resolves to the intended DigitalOcean
account. Fresh read-only name-filtered lists found no `pilot261004b`,
`pilot261005a`, `pilot261005c`, `norn-staging`, or `norn-management` matches in
Droplets, managed databases, load balancers, firewalls, VPCs, volumes,
snapshots, reserved IPs, or projects. `doctl` does not inventory Spaces buckets;
this is not complete account-zero evidence. Staging Fleet's offline
`scripts/sanity.py` validation passed. Its setup doctor still reports missing
staging provider/state secrets and runner configuration. The protected squash
metadata policy is now merged and passes staging doctor. The three remaining
groups are staging-plan secrets, staging secrets and protected runner
configuration. No cloud resource was created or changed.

The exact `pilot261004b` protected apply log confirms its pre-issued public
ingress TLS check passed before provider apply. The apply then remained in
managed database creation until exit 143 at 06:50:30 UTC. The signal sender is
unproven; do not attribute this interruption to the TLS gate.

These app and service counts include discovered and inactive entries; they do
not prove identity preservation through the earlier one-way transition or
health for every app.

Norn [PR #115](https://github.com/antiartificial/norn/pull/115) merged the bridge
title correction as `c56fa0c`. The signed
[platform-c56fa0c release](https://github.com/antiartificial/norn/releases/tag/platform-c56fa0cbef322d44abf905a423bea72df26292b2)
contains four verified bundles. Publication is not Mini promotion or Fleet
fitness. The local `codex/v3-release-integration` branch remains unmerged and
must not be treated as protected release authority. The main Norn checkout has
unrelated local changes that must be preserved.

## Pilot lineage

| Pilot | Most recent useful evidence | Disposition |
| --- | --- | --- |
| `pilot261003c` / `pilot261003d` | `c` and `d` exposed earlier gaps in provider evidence, TLS handoff and emergency cleanup. The `d` Fleet apply stopped before provider mutation; management was retired by emergency unprotected cleanup. | Historical failure evidence, not launch proof. Old `c`/`d` access items were retired in the later owner observation. |
| `pilot261004a` | Exact-SAN TLS and replacement GitHub App key supported complete management handoff and protected Fleet planning. Its apply stopped at the pre-provider retained-bucket inventory gate. Management and both backends were retired before its approved deadline. | Clean cloud/backend abort; two Tailscale device records and replacement OAuth client were still pending at the last observation. Recheck them separately. |
| `pilot261004b` | Protected plan `37273380169`, Norn plan `a25b25fa-c0b0-47e0-b9ec-836ce6cde3ed`, and apply `37273763546` reached live provider creation. State missed both databases. | Fleet never bootstrapped; emergency technical teardown occurred after the 08:30 UTC deadline. Technical access cleanup is observed; billing closure remains open. |

Pilot `a` was the first complete management-to-Fleet handoff, but `b` was the
most recent and exposed the material partial-apply failure. A fresh pilot
should be based on the repaired contracts, not resumed from `a` or `b`.

`pilot261005a` is a **proposed** fresh run ID, not an approved or active pilot.
At 16:20 UTC on 2026-10-05, read-only checks against the intended
`theartificial@hotmail.com` account found no matching droplets, databases,
projects, load balancers or VPCs; no registered GitHub runner carries its Fleet
label. The broader name checks also found no matching c-suffixed droplet,
database, project, load balancer, VPC, volume, firewall, reserved-IP or snapshot
names. These are name-collision checks, not a full provider-zero or
Spaces-bucket proof. The canonical owner-only `pilot261005a` directory, its
budget contract/activation files, and its DigitalOcean token file do not exist;
staging aggregate-budget variables still point to the retired `b` paths and
digest. Fleet [PR #255](https://github.com/antiartificial/norn-fleet/pull/255)
merged the signed c budget, plan, apply, recovery and exact topology gates as
`abb0b00`; it rejects reuse of retired `pilot261004b`. The PR contract check
and merge-main plan and validation passed (`37335410031`, `37336272298`,
`37336272528`). This is code validation, not live proof. Read-only checks at
16:20 found no c-named DigitalOcean resources in the categories above and no
registered c runner. The independent deadline retirement path remains
unimplemented. Existing approvals require post-create state and expire within
an hour, so signed budget expiry alone cannot guarantee teardown. The existing
owner-Mac retire-by watchdog performs only a local finalizer; it has no
provider, GitHub, Spaces or token-exchange capability. Fleet PR #256 contains a
validation-only descriptor checker, not a controller. Its runbook records that
root destroy plans must be built after resources exist. Fleet PR #256 commit
`1fcaeb7` adds a same-lock, ledgered backend cleanup start: a retry can
continue after the short approval expires only if the first start event bound
the complete exact authority while that approval was live. Full contract
validation passed on both `1fcaeb7` and `abaabc4`. This does not authorize a
new start after expiry or provide
Fleet/management deadline-time approvals. A read-only workflow audit confirmed
no existing workflow can mint post-create retirement approvals or execute the
ordered retirement chain. There is no installed or wired
deadline-retirement executable. The separate branch's coordinator and
delegation/journal primitives alone do not close this gate. Do not start a new
billable run until a
restartable, run-scoped owner-Mac controller
can fence the runner, perform Fleet → management → backend cleanup automatically
from T0+4, retain each stage's receipts, and continue retrying through and beyond
T0+5 until final zero. A timer alone is not a hard spend cap when the owner host
or provider access is unavailable.

At 19:20 UTC, the authenticated `doctl` context again identified the intended
DigitalOcean account. Name-filtered lists contained no `pilot261004b`,
`pilot261005a`, or `pilot261005c` matches: 2 droplets, 0 managed databases,
0 load balancers, 0 firewalls, 6 VPCs, 1 volume, 2 snapshots, 0 reserved IPs,
and 3 projects were listed. This is a scoped resource-name collision check;
Spaces buckets/object versions, DNS, GitHub runners, Tailscale devices, and
provider resources without these names were not proven absent. No mutation was
made.

## Current release gates (reconciled 2026-11-07)

This replaces the preceding percentage estimates, which predate the completed
Mini transition and latest DO Genesis attempt. The detailed ordered plan and
exit criteria are in [release program milestones](release-program-milestones.md)
and [release fitness plan](release-fitness-and-controller-plan.md).

| Lane/gate | Current state | Next proof |
| --- | --- | --- |
| Mini direct | V3 transition and recovery completed; independent node, not Fleet. | Keep this as the direct-lane reference. Measure interruption on future changes; do not claim uninterrupted or generally online migration. |
| DO staging Fleet / soak | 72h35m soak completed with 4,254 valid hash-chained samples and no recorded alarms/gaps. Fresh provider readback at 09:36 UTC confirms all nine expected Droplets active, the staging load balancer active, and the two staging managed databases online. | Fresh control-plane/node health, etcd diagnosis, service-route proof, host identity, and exact control roster. Provider status does not prove runtime or application health. |
| DO Genesis readback | Latest protected readback `42418784411` reached the management/control hosts, then failed read-only because a required capability or signed target commit is absent from the protected source repository. Inputs used Oct 21 state/route pins. | Diagnose object presence without mutation; refresh state/route/host-key evidence; only then issue a fresh one-use readback intent. |
| Existing DO Norn release | All-stop, forward-only controller is the supported release design. | Wait for fresh M3 readback and health gates; then execute exact-candidate release, postflight, and same-target recovery evidence. |
| Fresh DO Fleet | Future fresh-Fleet contract remains separate from staging. Current staging `main` source at `4451767624b12cbc41cc32e160a10d4b46006e0f` declares control 3, ingress 2, and one app pool at 4; the provider readback matches those nine nodes. It does not implement the future two-app-pool General + Stateful default. Genesis is a separate existing-Fleet controller-install path. | Complete the future General + Stateful pool contract, provider source, IDs/sizes/capacity bounds, placement semantics, and integrated workflow. Continue token-scope admission and per-file secret provenance/lifecycle. Persistent staging has no sanctioned whole-Fleet destroy path. |
| Workload resize | Fixed-image replica scale, app-pool resize, and mixed-version application release remain separate lanes. M5 has an offline stateless release contract, durable PostgreSQL/etcd receipt CAS, signed-acceptance verification, claim-bound route intents with independent proof and atomic promotion, target-health gating, and old-job stop/readback recovery. PR #302 (`c55efd56`) fixes the stop-ordering defect: mixed traffic cannot authorize stopping the old job; stop requires target-only route proof. Focused Go tests, all PR CI checks, and the bounded statechart pass. | PR #302 remains open and unmerged; it is not live-qualified. The operator API/CLI admission path is absent, so the internal worker is not yet usable as a release action. A production-store worker process/lease crash matrix, target-specific public traffic evidence, live canary qualification, and stateful data-compatibility proof remain open. Prior replica 2→3→2 and sequential app-node drains do not establish mixed-version release safety. See the [M5 protocol](m5-workload-release-protocol-2026-11-07.md). |
| Online Norn upgrade | Unsupported and not modeled. | Keep all-stop until compatibility, writer fencing, quorum, interruption and recovery contracts pass model and staging fault exercise. |
| V2 cleanup | No general `/api/v2` namespace; current V3 CLI/UI still call unversioned `/api`, and the protected `norn-fleet` runner still calls `/effect-grants/v2`. | Migrate callers route-by-route and qualify a replacement grant protocol; retain V2 read/recovery support for saved grants until its recovery window closes. See the [surface-retirement inventory](v2-surface-retirement-inventory-2026-11-07.md). |

No single percentage represents readiness across these independent lanes. The
DO Fleet is not currently confirmed healthy from fresh evidence, despite its
completed soak. No new protected release or provider mutation is authorized by
this status update.

The `doctl` `work` context returned the work-staging account UUID prefix
`17f40050` enforced by its preflight and was used for the provider readback
above. The current `norn-pilot` context is a different account (prefix
`05fe610a`) and must not be used for staging inventory. Provider inventory is
now confirmed, but a fresh protected Norn readback is still required for
control-plane, node, route, and application health. The failed Genesis
readback is not that proof and must not be retried with its expired intent.

## Evidence locations

- Owner-only `pilot261004b` handoff, raw Fleet and management state snapshots,
  two complete pre-cleanup inventories, run logs and exact-ID deletion records:
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/fleet-protected-handoff/`.
- Protected [Fleet apply](https://github.com/antiartificial/norn-fleet/actions/runs/37273763546)
  and [automatic recovery](https://github.com/antiartificial/norn-fleet/actions/runs/37274493490).
- Exact dry-run incident planner and tests in isolated Fleet checkout
  `/Users/arti/.config/norn/fleet-next-full-e2e/worktrees/incident-fleet-orphan-cleanup`
  at `eb7db92e5cb7f136472c5f43c2c4a8e5c9d4bed0`. It was not the executor
  used for the later emergency teardown and does not provide protected proof.
- `pilot261004a` abort and backend receipts under
  `/Users/arti/.config/norn/fleet-next-full-e2e/successor-pilot261004b/` and
  older pilot evidence under `/Users/arti/.config/norn/fleet-next-full-e2e/`.

The final invoice remains independent of the current technical zero
observation. The emergency cleanup did not produce a protected final-zero
receipt.
