# V3 platform release lanes and workload resizing

Status: operator guidance reconciled 2026-11-10 09:25 UTC against Norn `master` at
`2a2d712a`. The authenticated workload-release producer exists as an opt-in
staging API, but has not been deployed or live-qualified.
The Mini is an independent node. The DigitalOcean (DO) work Fleet is the only
Fleet and is a staging testbed intended to resemble production at lower scale.
A successful soak or retained provider state is evidence for the run it covers;
it does not prove that a service is healthy now.

The Mini now runs signed platform release `2a2d712a`; the existing A4
recovered-forward generation remains `candidate-promoted`, catalog schema
remains 52, and authenticated platform smoke passes. Two queued platform
upgrade attempts stopped before promotion at the host-agent stability gate; the
signed direct controller lane completed. A separate future-upgrade guard gap
was found: later releases must be checked as signed Git descendants while API
and host agent match the active SHA. Its code, offline regression, and
statechart are prepared but not yet qualified or deployed; the queue-upgrade
lane remains open.
Workload admission and resizing were not tested, and workload health remains a
separate claim. See the [latest launch evidence](LAUNCH-STATE.md#mini-direct-v3-platform-upgrade-completed-2026-11-10-0925-utc).

## Choose the lane

| Lane | Use | Current mechanism | Availability and recovery |
|---|---|---|---|
| Mini direct | Upgrade the independent Mini; deploy its app workloads from GitHub through Norn | Signed platform release and installed Mini upgrade controller. The active `2a2d712a` build includes push-commit-SHA pinning for GitHub/Gitea webhooks, but a fresh Mini push-to-deployment receipt and workload operation were not qualified. See [Mini Git-to-controller workload path](#mini-git-to-controller-workload-path). | The Nov 7 baseline transition completed with schema 51 and writer floor 33. On Nov 10, the signed direct controller lane promoted `2a2d712a`; schema stayed at 52, the earlier A4 recovery generation remained `candidate-promoted`, and platform plus queued smoke passed. Two queued upgrade attempts failed before promotion at the host-agent stability check; the historical subcondition remains unknown. A new guard correction for subsequent signed descendants is prepared but not live-qualified. Workload health remains mixed and workload admission/resize remain separate gates. See [latest launch evidence](LAUNCH-STATE.md#mini-subsequent-upgrade-guard-is-being-corrected-2026-11-10-0955-utc). Mini remains independent of the DO Fleet. |
| DO Fleet Norn release | Change Norn API/UI on the existing three-control DO Fleet | `work-staging-norn-release.yml`; exact publisher artifacts, fresh protected state readback, host key receipt, all-member staging, all-stop, target promotion, singleton verification, then durable pin update | Deliberate maintenance interruption. Forward-only recovery resumes the same target. Never restart old bytes after any target starts. |
| Fleet bootstrap | Create or recreate a Fleet | Protected provider `plan.yml`/`apply.yml`, then post-create reconciliation and Ansible. Protected `norn-fleet` main at `35e1c893` declares the existing DO staging shape: 3 control, 2 ingress, 4 app Droplets; the 18:58 UTC provider readback matched that shape. The current staging config remains one app pool (`workload: app`). Main supports a future two-pool General + Stateful role pair with stable IDs `general` and `stateful`, but no fresh-Fleet config exists yet; sizing, capacity, cost envelope, placement, and stateful recovery remain open. Do not conflate this with staging or Genesis, which installs a dormant controller on an existing Fleet without creating provider resources. See the [topology source audit](staging-topology-source-audit-2026-11-08.md). | Infrastructure lifecycle work, separate from platform release. The provider apply is not live-qualified for the future profile: joined persistent apply/recovery, exact provider identities, per-service secret lifecycle, actual DO grant evidence, post-create bootstrap, and terminal receipt remain open. |
| Workload resize/release | Add or remove application capacity or release a new application version | Fixed-image replica scaling and node-pool capacity are separate from the M5 mixed-version controller. The staging-only authenticated admission API accepts a signed V3 qualification ID and target replica count, then derives current policy, Fleet placement, active route, Nomad lineage, app source and encrypted SOPS snapshot. Accepted recovery input now pins the exact job-driving app spec plus encrypted SOPS bytes in a key-ring envelope; worker target construction no longer needs the current checkout. | The API is opt-in and not deployed or live-qualified. Offline acceptance/recovery evidence now includes store/worker tests and a real-etcd acceptance/open integration test. Full production executor/worker restart recovery, private-input retention/deletion, production-store process/lease crash matrix, and disposable Nomad/ingress qualification remain open. Live canary and stateful data-compatibility qualification are also open. Route-proof loss holds progress and requires fresh same-generation readback. Norn control binaries stay fixed unless separately released. |

The DO fleet's last confirmed Norn source inventory in the retained release
record was `31db2de8dafe14d234ac9e2f29c8480245dae7b4` on all three controls
(Nov 2). Protected state and host identity readback passed on Nov 7. A
read-only probe from the pinned management host to `/api/health` and
`/api/version` on each state-pinned private control address timed out the same
day. The timeout does not prove the droplets are down; it means current Norn
service health is unconfirmed. Public ingress does not route the private Fleet
API, so use the protected private inventory for a live health claim.

The latest `work-staging-norn-release` run
([42419247951](https://github.com/antiartificial/norn-fleet/actions/runs/42419247951))
was cancelled before its release job ran; the last successful run is
[41507907022](https://github.com/antiartificial/norn-fleet/actions/runs/41507907022)
from Nov 2. Protected raw backend-object readback
([42783948949](https://github.com/antiartificial/norn-fleet/actions/runs/42783948949))
passed for the exact retained state bytes and provider identity. It did not
establish current Norn/API, host-key, workload, or route health. The required
work-staging variable
`NORN_WORK_PLATFORM_GENESIS_READBACK_ENABLED` is absent. The Nov 10 provider plan
([42731843397](https://github.com/antiartificial/norn-fleet/actions/runs/42731843397))
was plan-only, and the protected private service inventory remains failed/held.
Refresh runner and controller pins, then obtain a fresh protected private
inventory before claiming the DO staging Fleet is online.

## Mini Git-to-controller workload path

The documented operator path for a configured app is `norn deploy APP COMMIT_SHA`.
Norn records the deployment operation and its worker runs the
deployment stages. Git supplies the source revision, and the configured app
name selects the workload. Older lab scripts that call `nomad job run`
directly bypass Norn admission and its operation recovery.

The PostgreSQL-backed app API already has `POST /api/webhooks/github` and
`POST /api/webhooks/gitea`. The handler verifies the provider HMAC, matches the
repository URL and branch to an app whose `repo.autoDeploy` is enabled, records
the delivery, and queues a durable `app.deploy` operation. The V3 etcd Fleet
router does not mount these routes.

A read-only Mini inventory on Nov 9 at 17:53 UTC reported API health `ok` on
source `d4dc88e0c37003a1013caed444983b2eeb50fbcb`. The running API process had
`NORN_WEBHOOK_SECRET` configured, and the delivery readback returned 25 recent
rows, all from GitHub (17 `deploying`, 8 `ignored`). Those are receipt states;
`deploying` does not prove the corresponding deployment operation succeeded.
The app inventory contained 30 records, including 12 records with
`repo.autoDeploy`; some apps have more than one repository mapping. Gitea has
no healthy allocation or service in that readback, so do not rely on it as a
live source host today.

A read-only GitHub hook audit found active `push` hooks on
`antiartificial/field-harbor`, `ft-trove`, `mail-agent`, `mail-indexer`,
`mail-mcp`, and `signal-sideband`. All six point to
`norn-webhooks.slopistry.com/api/webhooks/github`. The GitHub delivery API
returned no recent delivery rows for those hooks, so this verifies configured
destinations, not a successful current push-to-Mini deployment. Hooks were
absent for the other checked personal repositories `audio-scene-lab`,
`motifgarden`, and `norn-bloom`. GitHub does not return saved hook secrets;
the process-level check proves that the Mini has a secret configured, but not
that its value matches each GitHub hook. The current API accepts one
`NORN_WEBHOOK_SECRET` for all configured GitHub and Gitea hooks; it does not
provide per-repository secret isolation. Configure hooks to match that shared
secret and treat rotation as a coordinated change across the hooks. Repository
matching includes the Git host as well as owner/repository path, so an
unrelated host with the same path cannot select the app.

The webhook handler previously queued the branch ref. The active Mini release
includes PR #356 (`ccb49a5662a73a550274ce6d144bd5d6daf7491d`), which queues the
push event's `after` SHA, ignores branch deletion, and uses the stored SHA for
manual replay. Focused disposable-PostgreSQL tests prove the accepted operation
and replay retain that SHA. This source fix is deployed on Mini, but a fresh
push-to-Mini readback through receipt, durable operation, and workload
postflight has not been run. Do not claim the webhook path is end-to-end
qualified until that rehearsal succeeds.

For personal toy and sample projects, keep GitHub as the canonical remote.
Use one-way mirroring to Norn only when a local/private copy is useful; do not
make two independently writable remotes release authorities. The six hooks
above already point to Norn's webhook hostname. Before treating any one as a
working Mini deploy path, reconcile that hostname's ingress to this Mini,
confirm the hook uses the configured shared secret, and verify its exact app
mapping.
A hook requests deployment; Norn maps the repo and branch to one configured
app and records the resulting operation.

Progressive mixed-version release is a separate capability. The V3
`app.workload-release` HTTP producer is staging-only. It accepts a qualification
ID and target replica count, then derives an approved overlap policy, a Fleet
target, the current serving route, Nomad lineage, app source, and encrypted
SOPS input. It does not accept a Git repository or ref and cannot target the
independent Mini. The [M5 workload-release design](m5-workload-release-admission-design-2026-11-08.md)
and [protocol record](m5-workload-release-protocol-2026-11-07.md) describe that
separate Fleet lane.

Before relying on the Mini hook, verify the following behavior on a private
rehearsal. The hook must preserve explicit app selection and controller
recovery:

1. Configure each allowed repository and branch or tag to one Mini app and
   environment. Reject unknown repositories, refs, and app mappings.
2. Authenticate the Git host request and make provider delivery retries
   idempotent. Bind the request to its immutable push commit SHA. Treat the
   request as neither release approval nor artifact provenance.
3. Resolve the source against the Mini app's configured checkout. Run the
   deployment path's validation and artifact checks. Never accept a job spec,
   scheduler identity, route state, or secret value from the hook.
4. Submit the immutable source selection through the same controller
   deployment admission used by `norn deploy`. Reuse the stable provider
   delivery identity as the idempotency key. Confirm retries return the same
   operation and a changed payload conflicts.
5. Have the controller run the normal deployment stages, check target health,
   and update the route under its recovery contract. Keep operation and
   external-effect receipts across controller restart. Hold if readback is
   missing or ambiguous. Require the separate approved overlap protocol before
   promising mixed-version availability.
6. Report success to Git only after the operation reaches its successful
   terminal state. An accepted or queued response is not success.

The focused source test covers HMAC-backed acceptance, exact delivery replay,
conflicting payload rejection, missing commit SHA, branch deletion, and
same-SHA manual replay. Keep the broader gates open until tests cover unknown
repo/ref mappings, checkout changes after acceptance, controller restart, and
lost scheduler or route responses. Then verify the Mini endpoint and webhook
configuration in a private rehearsal. The Mini now runs a build containing the
source-pinning fix merged as `ccb49a56…`, but the hook-to-receipt-to-operation
path has not been exercised after the upgrade. Do not claim live exact-push
Mini webhook support until that private rehearsal passes. Do not claim
mixed-version Mini availability without the separate overlap protocol and
qualification.

## DO Norn release protocol

The release intent binds exactly:

- installed source SHA and its successful publisher run;
- target source SHA and its successful publisher run;
- fresh protected state readback run;
- acknowledgement `upgrade-<target-sha>-from-<old-sha>`.

The controller verifies the owner signature, expiry, exact workflow, exact
release job and labels, deterministic run title, source/run bindings and one-use
runner identity before starting the JIT runner. The remote job gate retains its
strict five-field release binding. The controller journal binds that exact
binding to the signed intent; the workflow's run title carries the same old,
target and readback identities. Dispatch is write-once: if GitHub's response is
lost, reconcile the exact run by title and journal instead of dispatching a
second run.

Execution stages and hashes target artifacts on all three controls while the
old singleton remains available. It then stops all old services, verifies all
listeners are closed, promotes target bytes on every stopped control, starts
target services and checks singleton API plus etcd health. Only then are
bootstrap API/UI pins atomically advanced. A later provider apply must use a
fresh plan because pin changes invalidate old plan evidence.

Recovery is forward-only and idempotent for exact old/target bytes. Resume the
same signed target after reclassifying all controls. Preserve the durable
journal on ambiguous runner or host outcomes. A third source, digest, UI
selection, droplet identity, or host key is a hold for inspection. Never
reverse-write pins or start an old binary after any target has started. Retire
the one-use runner only after terminal evidence and verify its GitHub
registration is removed.

Until the intent route and the matching controller build are installed on the
management Mac, the protected workflow is not hands-free. Do not describe its
manual GitHub dispatch as controller-backed. The offline intent tests and
state model qualify contracts; they do not prove a live DO release.

## Workload resizing with mixed workload versions

Keep application deployment versions distinct from Norn platform versions.
A workload may run old and new allocations together while Norn remains fixed,
provided the workload's compatibility declaration and data migration support
that overlap. The current DO Norn control release is all-stop; mixed Norn
control versions have not been qualified and are not permitted by this runbook.

For an online workload resize or workload release:

1. Read current pool capacity, allocations, health, drain state, database
   schema, and exact workload image/source digests. Check the compatibility
   matrix for old and new application versions and every shared reader/writer.
2. Add capacity first when placement needs it. Require the new nodes to be
   registered, healthy, schedulable and represented in the exact inventory
   before moving allocations.
3. Expand data contracts additively. Keep old and new binaries able to read
   the expanded schema; gate writes by an explicit schema/writer epoch. Backfill
   in bounded, restartable batches and record checkpoints.
4. Deploy a small target cohort. Verify readiness, error rate, data invariants,
   queue/worker progress and route behavior. Increase traffic only after each
   checkpoint. Keep old allocations serving while target allocations warm.
5. Drain old allocations with workload acknowledgements and a bounded
   disruption budget. Do not shrink the pool while pending placements or
   required quorum/replica counts depend on it.
6. Observe the mixed-version window. Retire old allocations only after target
   health, data checks and writer inventory pass. Remove old schema contracts
   in a later release after the compatibility window expires.
7. Shrink capacity last. Verify no allocation is stranded, desired and actual
   counts agree, and the pool returns to its prior cost/shape target.

At every phase, persist the intent, source inventory digest, target image
identity, schema epoch, placement decision, drain receipt and last completed
checkpoint before the side effect. Retrying the same intent must converge on
the same target. If target health or route proof fails, hold drains and schema
contraction while preserving the last published route. A switch back to old
allocations requires a separately admitted intent and fresh proof that the old
generation is healthy and remains compatible with current data and writer
epochs. After the writer/schema fence moves, resume forward or restore from an
explicitly tested recovery point; changing an image or symlink does not reverse
data writes.

## Qualification required for online Norn platform upgrades

The existing DO workflow deliberately uses all-stop because no mixed Norn
control compatibility window is qualified. An online control-plane protocol
must first prove:

- old/new API read/write compatibility for the same control-store schema;
- an additive migration and explicit minimum reader/writer epochs;
- active/standby role discovery and fencing so exactly one API writer exists;
- one-member-at-a-time target activation while etcd quorum remains healthy;
- rollback rules before the compatibility fence and forward-only recovery after
  it;
- API/etcd health, client behavior, and measured interruption during failures at
  every boundary.

Do not infer online safety from Nomad workload resizing. Qualify the Norn
control transition separately on the DO testbed before offering an online
platform lane.
