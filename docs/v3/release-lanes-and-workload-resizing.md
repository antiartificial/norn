# V3 platform release lanes and workload resizing

Status: operator guidance reconciled 2026-11-12 16:54 UTC against the release
ledger and Fleet main; the latest read-only DO runtime observation recorded in
the ledger is 16:26 UTC, and Fleet runbook PR #1028 merged at 16:54 UTC as
`11e4611c`. The DO platform release and controller recovery are recorded in
the launch ledger. The authenticated workload-release producer exists as an
opt-in staging API, but has not been deployed or live-qualified.
The Mini is an independent node. The DigitalOcean (DO) work Fleet is the only
Fleet and is a staging testbed intended to resemble production at lower scale.
A successful soak or retained provider state is evidence for the run it covers;
it does not prove that a service is healthy now.

The Mini runs signed platform release `34178b8b`; the existing A4
recovered-forward generation remains `candidate-promoted`, and catalog schema
remains 52. At 15:26 UTC, its API health returned HTTP 200 and its
Norn-managed `vigil-gateway` allocation returned HTTP 200 from `/health`. This
meets the owner's criterion for a successful Mini direct deployment: V3 is
active and at least one workload responds. Three queued upgrade attempts
stopped before promotion at the host-agent stability gate; diagnose that
recovery path separately. Git-to-Norn workload admission remains unqualified,
and workload resizing is not a Mini lane. See the
[current launch evidence](LAUNCH-STATE.md#current-mini-status-2026-11-10-1526-utc).

## Choose the lane

| Lane | Use | Current mechanism | Availability and recovery |
|---|---|---|---|
| Mini direct | Upgrade the independent Mini; deploy its app workloads from GitHub through Norn | Signed platform release and installed Mini upgrade controller. The active `34178b8b` build includes push-commit-SHA pinning for GitHub/Gitea webhooks, but a fresh Mini push-to-deployment receipt and workload operation were not qualified. See [Mini Git-to-controller workload path](#mini-git-to-controller-workload-path). | The Nov 7 baseline transition completed with schema 51 and writer floor 33. On Nov 10, the direct controller lane promoted `2a2d712a`, then advanced to signed `34178b8b`; schema stayed at 52 and platform smoke, host status, and doctor passed. Three queued upgrades stopped before promotion at the completed-recovery host-agent stability check; the latest receipt reported no child under the LaunchAgent wrapper. The direct success does not qualify queued upgrades. The cause remains unexplained; the offline model now covers an absent-agent stability reset. The direct Mini deployment meets the stated success criterion: its V3 API and a Norn-managed workload both respond. Broader app health and Git-driven admission remain separate follow-ups. See [current launch evidence](LAUNCH-STATE.md#current-mini-status-2026-11-10-1526-utc). Mini remains independent of the DO Fleet and has no Fleet resize lane. |
| DO Fleet Norn release | Change Norn API/UI on the existing three-control DO Fleet | `work-staging-norn-release.yml`; exact publisher artifacts, fresh protected state readback, host key receipt, all-member staging, all-stop, target promotion, singleton verification, then durable pin update | Deliberate maintenance interruption. Forward-only recovery resumes the same target. Never restart old bytes after any target starts. |
| Fleet bootstrap | Create or recreate a Fleet | Protected provider `plan.yml`/`apply.yml`, then post-create reconciliation and Ansible. Protected `norn-fleet` main at `11e4611c` still declares the DO staging shape: 3 control, 2 ingress, 4 app Droplets, plus two management nodes. The 2026-11-12 16:26 UTC provider inventory found all eleven active. The current staging config remains one app pool (`workload: app`). Main supports a future two-pool General + Stateful role pair with stable IDs `general` and `stateful`, but no fresh-Fleet config exists yet; sizing, capacity, cost envelope, placement, and stateful recovery remain open. Do not conflate this with Genesis, which installs a dormant controller on an existing Fleet without creating provider resources. See the [topology source audit](staging-topology-source-audit-2026-11-08.md). | Infrastructure lifecycle work, separate from platform release. The provider apply is not live-qualified for the future profile: joined persistent apply/recovery, exact provider identities, per-service secret lifecycle, actual DO grant evidence, post-create bootstrap, and terminal receipt remain open. |
| DO workload resize/release | Add or remove application capacity or release a new application version on the DO staging Fleet | Fixed-image replica scaling and node-pool capacity are separate from the M5 mixed-version controller. The staging-only authenticated admission API accepts a signed V3 qualification ID and target replica count, then derives current policy, Fleet placement, active route, Nomad lineage, app source and encrypted SOPS snapshot. Accepted recovery input pins the exact job-driving app spec plus encrypted SOPS bytes in a key-ring envelope. | The API is opt-in and not deployed or live-qualified. The target-stage restart statechart passes 8 safety/deadlock checks (26 states, 31 transitions); mixed-version protocol models pass their bounded checks at capacities 3 and 4, with capacity 3 unable to reach target-only routing or terminal receipt for two-old/two-target allocations. At 16:48 UTC, a disposable local etcd/Nomad integration test dropped Nomad's successful target-create response, hid the first readback, restarted the etcd client, and proved successor recovery bound the existing job revision without duplicate registration. Separate route/drain restart tests still use a test driver for external effects. The 16:26 DO canary `/route-proof` and `/version` returned HTTP 200, but public responses do not prove signed admission; the earlier failed deployment wait still lacks a terminal receipt readback. Full private-input staging, target health/ingress, old-cohort stop, same-intent durable admission receipts, controller-admitted DO release, and live mixed-version resize remain open. Route-proof loss holds progress and requires fresh same-generation readback. Stateful data compatibility is separate. Norn control binaries stay fixed unless separately released. This lane applies to DO only. |

The controller-mediated DO platform release completed on 2026-11-11 through
the forward-only all-stop path. Protected state readback [42943628535](https://github.com/antiartificial/norn-fleet/actions/runs/42943628535)
passed; upgrade [42945021070](https://github.com/antiartificial/norn-fleet/actions/runs/42945021070)
verified the signed target, singleton private API backed by healthy etcd, and
durable API/UI pins on all three controls. Service was interrupted during
promotion. This is platform-release evidence; it does not qualify workload
admission, workload response, mixed-version resize, or an online upgrade.

The 11:17 UTC Nov 12 read-only `doctl --context work` inventory reported all
eleven DO staging Droplets active in `nyc3`. The Nov 11 protected postflight
verified the target Norn release and API/etcd health on the three controls.
At 11:17, the public platform endpoint returned HTTP 200, and the separate
canary returned HTTP 200 for `/route-proof` and `/version`; five version reads
reached two allocations on two nodes. This proves one public workload is
serving, not current Nomad/Consul or private API health, V3 admission, or a
durable terminal deployment receipt. Use fresh protected private evidence for
control-plane claims; public ingress does not route the private Fleet API.

Protected Genesis intent/journal readback [42898054171](https://github.com/antiartificial/norn-fleet/actions/runs/42898054171)
passed and confirmed the approved intent and dormant journal. It is a
read-only check, not Genesis execution or fresh-Fleet creation. The later
protected state handoff [42943628535](https://github.com/antiartificial/norn-fleet/actions/runs/42943628535)
and platform release are recorded above. Earlier failures at
[42827741033](https://github.com/antiartificial/norn-fleet/actions/runs/42827741033)
and [42831291445](https://github.com/antiartificial/norn-fleet/actions/runs/42831291445)
remain dated history and are superseded for that readback gate. Fresh-Fleet
provider creation, Genesis/bootstrap recovery, and the two-pool sizing and
credential contract remain separate open work.

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
