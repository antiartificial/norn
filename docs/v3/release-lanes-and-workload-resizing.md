# V3 platform release lanes and workload resizing

Status: operator guidance updated 2026-11-09 against merged Norn `master` at
`877ad4ba`. The authenticated workload-release producer exists as an opt-in
staging API, but has not been deployed or live-qualified.
The Mini is an independent node. The DigitalOcean (DO) work Fleet is the only
Fleet and is a staging testbed intended to resemble production at lower scale.
A successful soak or retained provider state is evidence for the run it covers;
it does not prove that a service is healthy now.

## Choose the lane

| Lane | Use | Current mechanism | Availability and recovery |
|---|---|---|---|
| Mini direct | Upgrade the independent Mini; optionally deploy its app workloads from an in-Norn Git host | Signed Norn release and installed Mini upgrade controller for the platform. A future workload path may bind a hosted Git repository/hook to the Mini's independent controller. | The Nov 7 controller-mediated V3 transition completed at `bba290db041e9f497c7161543c679d9d348b742d` with schema 51 and writer floor 33; a signed repair recovered a PID-reuse race. On Nov 8, the Mini advanced to signed RC `d4dc88e0c37003a1013caed444983b2eeb50fbcb`; smoke and host doctor passed. This proves those forward transitions, not uninterrupted service or general online migration support. The repo does not yet establish a qualified Git-host-to-Mini workload hook. Treat it as a separate lane to wire: repository identity and branch/ref must map explicitly to the Mini app/environment/controller, and the hook must submit a qualified operation to that target. A Git push alone must never infer a deployment target. |
| DO Fleet Norn release | Change Norn API/UI on the existing three-control DO Fleet | `work-staging-norn-release.yml`; exact publisher artifacts, fresh protected state readback, host key receipt, all-member staging, all-stop, target promotion, singleton verification, then durable pin update | Deliberate maintenance interruption. Forward-only recovery resumes the same target. Never restart old bytes after any target starts. |
| Fleet bootstrap | Create or recreate a Fleet | Protected provider `plan.yml`/`apply.yml`, then post-create reconciliation and Ansible. Protected `norn-fleet` main at `4451767624b12cbc41cc32e160a10d4b46006e0f` declares the existing DO staging shape: 3 control, 2 ingress, 4 app Droplets; provider readback matches that shape. There is one app pool (`workload: app`). A future fresh-Fleet default of two pools (General + Stateful) is agreed but not yet encoded, and must not be conflated with current staging. Genesis installs a dormant controller on an existing Fleet; it does not create provider resources. See the [topology source audit](staging-topology-source-audit-2026-11-08.md). | Infrastructure lifecycle work, separate from platform release. Apply binds a reviewed plan and one-use capacity dispatch; partial-create recovery, complete secret provenance, and DO-token permission-scope admission still need integrated offline qualification. |
| Workload resize/release | Add or remove application capacity or release a new application version | Fixed-image replica scaling and node-pool capacity are separate from the M5 mixed-version controller. The staging-only authenticated admission API accepts a signed V3 qualification ID and target replica count, then derives current policy, Fleet placement, active route, Nomad lineage, app source and encrypted SOPS snapshot. Accepted recovery input now pins the exact job-driving app spec plus encrypted SOPS bytes in a key-ring envelope; worker target construction no longer needs the current checkout. | The API is opt-in and not deployed or live-qualified. Offline acceptance/recovery evidence now includes store/worker tests and a real-etcd acceptance/open integration test. Full production executor/worker restart recovery, private-input retention/deletion, production-store process/lease crash matrix, and disposable Nomad/ingress qualification remain open. Live canary and stateful data-compatibility qualification are also open. Route-proof loss holds progress and requires fresh same-generation readback. Norn control binaries stay fixed unless separately released. |

The DO fleet's last confirmed Norn source inventory in the retained release
record was `31db2de8dafe14d234ac9e2f29c8480245dae7b4` on all three controls
(Nov 2). Protected state and host identity readback passed on Nov 7. A
read-only probe from the pinned management host to `/api/health` and
`/api/version` on each state-pinned private control address timed out the same
day. The timeout does not prove the droplets are down; it means current Norn
service health is unconfirmed. Public ingress does not route the private Fleet
API, so use the protected private inventory for a live health claim.

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
