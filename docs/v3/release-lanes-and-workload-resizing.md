# V3 platform release lanes and workload resizing

Status: operator guidance for the capabilities verified through 2026-11-07.
The Mini is an independent node. The DigitalOcean (DO) work Fleet is the only
Fleet and is a staging testbed intended to resemble production at lower scale.
A successful soak or retained provider state is evidence for the run it covers;
it does not prove that a service is healthy now.

## Choose the lane

| Lane | Use | Current mechanism | Availability and recovery |
|---|---|---|---|
| Mini direct | Upgrade the independent Mini | Signed Norn release, local protected backup and candidate shadow restore, then the installed Mini upgrade controller | The Nov 7 controller-mediated V3 transition completed at `bba290db041e9f497c7161543c679d9d348b742d` with schema 51 and writer floor 33; a signed repair recovered a PID-reuse race. On Nov 8, the Mini advanced to signed RC `d4dc88e0c37003a1013caed444983b2eeb50fbcb`; smoke and host doctor passed. This proves those forward transitions, not uninterrupted service or general online migration support. |
| DO Fleet Norn release | Change Norn API/UI on the existing three-control DO Fleet | `work-staging-norn-release.yml`; exact publisher artifacts, fresh protected state readback, host key receipt, all-member staging, all-stop, target promotion, singleton verification, then durable pin update | Deliberate maintenance interruption. Forward-only recovery resumes the same target. Never restart old bytes after any target starts. |
| Fleet bootstrap | Create or recreate the DO staging Fleet | Protected provider `plan.yml`/`apply.yml`, then post-create reconciliation and Ansible. The checked-in `staging/nyc3` source declares 3 control and 2 ingress Droplets (5 total), with ingress allowed to scale to 3 for a rehearsal; managed databases are separate. It declares no app node pools. The future fresh-Fleet default is two app pools: General and Stateful. This choice is agreed, but the provider source and sizing contract are not yet updated. Genesis installs a dormant controller on an existing Fleet; it does not create provider resources. See the [topology source audit](staging-topology-source-audit-2026-11-08.md). | Infrastructure lifecycle work, separate from platform release. Apply binds a reviewed plan and one-use capacity dispatch; partial-create recovery, complete secret provenance, and DO-token permission-scope admission still need integrated offline qualification. |
| Workload resize | Add or remove application capacity or release a new application version | Fixed-image replica scaling and node-pool capacity are separate from the internal M5 mixed-version controller. It has signed-acceptance checks, durable progress, claim-bound route publication/proof/promotion, health gates, and stop/readback recovery. | The operator API/CLI admission path and production-store process/lease crash matrix are open, so M5 is not yet an operator-facing release lane. Live canary and stateful data-compatibility qualification are also open. Route-proof loss holds progress and requires fresh same-generation readback. Norn control binaries stay fixed unless separately released. |

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

## Release-readiness milestones

Use these gates to keep independent work moving while preserving the boundary
between offline proof and a live mutation. A pass at one milestone does not
implicitly approve the next one.

| Milestone | Deliverable and acceptance evidence | Can proceed alongside |
|---|---|---|
| R0 — Source and live-state baseline | Reconcile current PR heads, signed releases, Mini runtime, DO provider inventory, retained Fleet state, and protected controller/Genesis source pins. Label each item as current, historical, candidate, or unverified. Never promote provider inventory to service-health evidence. | All offline design and test work. |
| R1 — Deterministic controller war-game | Existing lane models cover Mini direct, DO release, Genesis, fresh-Fleet recovery, workload mixed versions, route publication, and stop recovery; see the [statechart index](statecharts/README.md). The nine-node and two-app-pool models pass their recorded bounded checks, but abstract credentials and inputs. Complete deterministic fixtures for intent admission, signature/expiry/one-use binding, provenance and credential transformations, journal checkpoints, retries, ambiguous outcomes, and terminal cleanup. Accept only when interruptions at every side-effect boundary and invalid/stale/duplicate inputs produce repeatable safe decisions. | R2 and R3 contract/design work. |
| R2 — Fresh-Fleet bootstrap contract | The agreed roles are already recorded as General + Stateful in the [input contract](fresh-fleet-two-app-pool-contract.md) and [two-pool model](statecharts/fresh-fleet-two-app-pools/README.md). Finish stable pool IDs, SKUs, capacity and cost bounds, placement, workload/recovery contracts, and input provenance; then connect the deterministic fixtures to the actual plan/apply parser and dry-run inventory reconciliation. Accept when every required input has a named source, scope, transformation, consumer, expiry/rotation policy, and redacted evidence check. | R1; no actual provider create is needed. |
| R3 — Workload resize and mixed versions | Complete operator-facing API/CLI admission and durable production-store crash/lease tests for M5; test route-proof loss, health regression, restart/replay, target-only promotion, and safe hold. Add a staging workload canary with compatibility/data checks before claiming online resize readiness. | R1, R2, and R4 implementation/review. |
| R4 — DO staging Fleet health and Norn release gate | Resolve Genesis source-object availability against fresh state, then obtain a fresh protected readback. Separately establish current private API, member, quorum, workload, ingress and route health. Only after those gates pass may a new signed intent drive the documented all-stop Norn release; retain terminal evidence and test forward recovery. | R1 and R3 offline work. |
| R5 — Mini direct recovery and assurance | Keep Mini independent. Confirm the installed signed source and health, refresh host assurance/routes, classify queued Fleet dispatch receipts, and retain a tested backup/restore and controller-recovery record. The Nov 8 smoke/doctor evidence covers platform checks, not stale route assurance or open incidents. | R1 and R3. |
| R6 — Release acceptance | Publish a lane-by-lane evidence bundle with immutable source/run IDs, state and host-key bindings, compatibility floors, observed interruption, recovery outcome, and explicit unresolved limits. Accept V3 as hands-free only for lanes whose complete input-to-terminal controller path has passed both deterministic war-game and target-specific live evidence. | Starts after R0–R5; documentation can be prepared earlier. |

R1–R3 are primarily offline and can be developed concurrently. R4 and R5
require target-specific live evidence, but their documentation and read-only
preflight can proceed in parallel. Do not spend an infrastructure run to
discover a missing credential, stale pin, absent source object, or unsupported
state transition that can be caught by R1–R3.
