# Fleet operations

This is the normative operator runbook for Norn Fleet. It describes the
control-plane contract and the boundaries an operator must keep intact. The
private `norn-fleet` repository remains the authoritative source for provider
resources and its own workflow instructions. See [Fleet GitOps](../infrastructure/fleet.md)
for the document schema and repository/bootstrap details.

## Terms and ownership

These names describe different changes. Do not use a successful result in one
lane as proof for another.

| Term | What changes | Authority and completion proof |
|---|---|---|
| Platform release | Norn API, CLI, UI, and host release | The signed, exact-SHA Norn release; platform preflight, upgrade, smoke, and host assurance |
| Fleet plan | Reviewed desired infrastructure change | A durable Norn capacity-plan receipt and its source/plan digests; this does not call a provider |
| Fleet apply | Provider and node-lifecycle execution of a Fleet plan | The protected infrastructure workflow, a numbered Norn runner attempt, and required checkpoints |
| App deploy | A Nomad/Norn application rollout | Its separate durable app operation, deployment steps, and application smoke result |

Norn owns schema validation, capacity-plan receipts, PR/dispatch provenance,
attempt history, checkpoint validation, inventory observation, enrollment,
readiness, drain evidence, and operator visibility. The private Fleet
repository and its protected runner own OpenTofu state, provider credentials,
VPC/load-balancer/firewall/VM mutation, and runner-local bootstrap secrets.
Nomad owns application allocation. There is no always-running Norn Fleet daemon
and a Norn plan never calls a cloud provider.

The roles are deliberately separate:

| Role | May do | Must not be treated as |
|---|---|---|
| Application developer | Change application code/InfraSpec, run local contract checks, and submit reviewed changes | A holder of provider, state, signing, or production-apply authority |
| Fleet reviewer | Review a digest-bound PR and plan artifact; approve the repository's protected process | Provider execution proof |
| Fleet administrator | Own the Fleet document, create plans, recover deterministic PR/apply dispatch, inspect evidence, and make supported recovery decisions | A substitute for protected-runner credentials or independent review |
| Release/security administrator | Own signing trust, GitHub/OIDC policy, access policy, and production qualification/promotion | Authorization to mutate provider state outside the Fleet lane |
| GitHub App | Repository-scoped PR and workflow-dispatch bridge | A personal GitHub token or provider credential |
| Protected runner | Apply reviewed infrastructure and emit bound liveness/checkpoint evidence | A broad control-plane administrator |
| Norn control plane | Validate and reject unsafe/out-of-order control actions | A cloud provider or Terraform state owner |

## Current, desired, and execution state

Keep these states distinct:

1. **Desired Fleet document** is versioned configuration: pool `min`,
   `desired`, `max`, instance size, region, and replacement policy.
2. **Current plan input** is the baseline stored in a capacity-plan receipt.
   It is the reviewed before-state for that plan, not a provider inventory.
3. **Proposed plan input** is the requested desired state or replacement.
4. **Observed state** is evidence from the provider workflow, generated
   inventory, node enrollment, Nomad/Consul, drain, and assurance. It can lag
   and must be identified by its checkpoint/evidence rather than guessed.
5. **Attempt state** is the durable runner execution record: its identity,
   current phase, revision, lease/heartbeat, status, retry lineage, and bound
   workflow URL.

In particular, a configured pool split such as control `3` plus ingress `2`
does not prove that five provider nodes already exist. A provider-empty
reconcile cannot be inferred from per-pool desired values alone. Treat a plan
as drift/reconciliation unless reviewed runner input proves a supported
operation class and exact created-node count.

## The five truths

Do not declare a Fleet change complete until the relevant truths agree:

1. **Git desired-state truth:** the deterministic PR merged through protected
   `main`, and the Fleet document plus plan artifact are for that exact SHA.
2. **OpenTofu state truth:** the protected runner reads the expected backend,
   lineage, serial, and lock state. Remote state records managed objects; it is
   not by itself proof that they still exist or are healthy.
3. **Norn evidence truth:** the plan receipt, numbered attempt, retry lineage,
   and append-only checkpoints bind intent and execution to the same commit,
   plan digest, provider state serial, and evidence digests.
4. **Provider inventory truth:** a read-only provider query proves which tagged
   resources actually exist and remain billable, including unmanaged or
   orphaned resources absent from OpenTofu state.
5. **Runtime truth:** node configuration/enrollment, Nomad/Consul placement,
   required drain evidence, host assurance, and user-facing checks prove that
   the resulting infrastructure is usable.

A queued dispatch, a green planning job, a heartbeat, or an app deploy alone
is not a substitute for all applicable truths.

## GitHub environments and permission boundary

Use the four protected GitHub Environments implemented by `norn-fleet`:

| Boundary | Purpose | Why it is separate |
|---|---|---|
| `staging-plan` | Read staging state and produce the reviewed plan artifact | Keeps review inputs separate from staging mutation/bootstrap authority |
| `staging` | Apply or recover an already-bound staging plan | Makes staging the exercised operational path without granting production access |
| `production-plan` | Read production state and produce the reviewed plan artifact | Keeps production review evidence and credentials independent of staging |
| `production` | Apply or recover an already-bound production plan | Contains production mutation/bootstrap authority and its independent approval policy |

`recover` is an allowed workload-identity intent inside the corresponding
`staging` or `production` environment, not a fifth GitHub Environment. Its
workflow identity and original dispatch binding prevent recovery authority from
becoming an unreviewed fresh apply.

The GitHub App needs only repository permissions required to create/recover the
Fleet PR and dispatch (`Actions: write`, `Contents: write`, `Pull requests:
write`, plus implicit metadata read). It never receives provider/state secrets.
The protected runner authenticates to Norn with a GitHub Actions OIDC workload
identity carrying the narrowly scoped `fleet:operate` authority. Do not use a
broad reusable runner token or a general `api:write` token for provider work.

Pin every action and reusable workflow reference in **both** `apply` and
`recover` workflows to an immutable full commit SHA. A branch, tag, or moving
`main` reference is not sufficient for a workflow that can access state or
provider credentials. The runner must record the reviewed commit, plan digest,
source dispatch run, and workflow URL before mutation.

Production remains inactive/unqualified until production admission, protected
environment policy, signed/audited planning, recovery rehearsal, and the
required availability/drain assurances have been proven. It is not promoted
merely because the same configuration works in staging.

## Golden sequence

Staging is the normal quickstart and rehearsal lane. Substitute the actual
staging cluster path and pool for the examples.

```sh
norn fleet validate environments/staging/nyc3/cluster.yaml
norn validate --file ./infraspec.yaml --fleet environments/staging/nyc3/cluster.yaml
norn fleet pools
norn fleet reconcile control --reason "bootstrap staging from protected main"
norn fleet github pr PLAN_UUID
# Review and merge the generated PR; wait for the exact-main plan artifact.
norn fleet github apply PLAN_UUID
norn fleet checkpoints PLAN_UUID
```

## Disposable pilot bridge

The disposable bridge is opt-in and intentionally narrower than the staging
lane. Configure the external staging authority with exactly these values (plus
the ordinary GitHub App fields):

```text
NORN_FLEET_GITHUB_ENVIRONMENT=staging
NORN_FLEET_GITHUB_CONFIG_PATH=environments/disposable/fleet/nyc3/cluster.yaml
NORN_FLEET_GITHUB_PILOT_RUN_ID=<8-to-24 lowercase alphanumeric characters>
```

Norn rejects every other disposable path, any non-staging control plane, and
an absent or malformed run ID. Its GitHub status response and CLI surface the
configured run ID so an operator can verify the lane before opening a PR.
The protected apply dispatch carries that same ID, and Norn persists it beside
the plan run, artifact digest, main commit, and nonce hash. A later change to
the configured pilot run cannot recover or re-dispatch the older binding.

After merging the deterministic Norn PR, dispatch the `plan.yml` workflow from
protected `main` with `fleet_environment=disposable/fleet/nyc3` and the exact
same `pilot_run_id`; unlike staging and production, a disposable plan is not
created by the ordinary push matrix. Wait for its successful artifact before
running `norn fleet github apply PLAN_UUID`. The workflow verifies the pilot ID
in that artifact again and maps disposable work to the existing `staging`
GitHub Environment. Do not substitute a new pilot ID or use the staging root.

Inspect the numbered runner attempt in the Fleet UI or through
`GET /api/v1/fleet/plans/{planID}/attempts`. Human CLI commands do not mutate
runner attempts. The fixed staging pilot has no `app` pool; use `plan app` only
after a separately reviewed Fleet document introduces that pool.

Before an apply, establish these preconditions:

- the authenticated principal, Fleet environment, repository, document digest,
  plan ID, commit SHA, and plan SHA all match the reviewed records;
- no active incompatible Fleet attempt or other change is holding the relevant
  state/maintenance window;
- the plan is within declared pool bounds, and any replacement/downsize has
  its explicit acknowledgement and required live capacity/drain evidence;
- provider state, locks, runner image/tool versions, and cloud-init/hooks are
  the reviewed versions; and
- the workflow ref is pinned, the environment is the intended one, and the
  runner will create an attempt before provider mutation.

For production, repeat the same sequence only after the production qualification
above is current. Use the exact protected workflow/commit and do not substitute
a local checkout, a branch head, or a manual provider console operation.

## Attempts, checkpoints, and timing

A protected runner creates one numbered attempt before mutating infrastructure.
The attempt binds external runner identity, reviewed commit and plan digest,
workflow URL, source dispatch, retry lineage, current phase, and optimistic
revision. Only one attempt may be live for a plan. Heartbeats are liveness
signals only: they extend neither authorization nor completion proof.

For each successful phase, append a checkpoint bound to that attempt and the
current phase, then advance the attempt. Checkpoints are append-only and carry
the reviewed commit, state serial/evidence digest, and applicable inventory,
configuration, enrollment, readiness, or drain proof. A failed checkpoint
fails the attempt; an out-of-order checkpoint, changed binding, or missing
replacement drain proof is rejected.

The phase order is evidence-gated. The exact list can depend on the plan, but
the destructive path uses:

`prechange_verified` → `provider_applying` → `infrastructure_applied` →
`inventory_generated` → `nodes_configured` → `nodes_enrolled` →
`readiness_verified` → `old_nodes_drained` → `complete`.

Non-destructive paths omit only phases that do not apply; never mark omitted
proof as successfully completed. An interrupted workflow resumes from the
first phase lacking successful evidence, not from its log position.

Timing is advisory. Where the protected runner has reviewed summary evidence
that the operation is exactly a five-node cold start, the attempt may show the
configured 15–30 minute total range measured from runner start. It is
low-confidence `configured_range`, not a provider SLA or authorization gate.
The active response can project elapsed and clamped remaining/completion
ranges; success reports actual elapsed, while failed/cancelled/abandoned
attempts retain the configured total but omit a remaining/completion promise.

An `unknown` or legacy/missing classification is explicitly unavailable. Do not
infer cold start from desired counts, a provider-empty observation, queue time,
or a plan that cannot prove five creates. The estimate excludes
`review_approval`, `github_queue`, `dns_propagation`, and
`application_migrations`; report those separately to operators.

Set expectations by milestone rather than presenting a single end-to-end ETA:

| Milestone | Expectation |
|---|---|
| Local validation and pull-request contract | Non-provider developer feedback; measure CI separately |
| Review approval and GitHub queue | External/unbounded; excluded from Fleet cold-start timing |
| Exact five-node staging cold start | Provisional 15–30 minutes from protected-runner start |
| First host-key or DNS identity gate | May stop safely and require provenance-linked recovery after out-of-band verification |
| Routine scale/no-op/recovery | No reliable duration until comparable successful samples exist |
| Application rollout and migrations | Separate Norn app operation and timing record |

## Recovery decision table

| Observed condition | Operator action | Resume point / boundary |
|---|---|---|
| Apply is running and heartbeats are current | Observe attempt/checkpoints; do not launch another runner | Existing live attempt owns the plan |
| Dispatch response was lost but the deterministic workflow/run exists | Reissue the Norn apply request to recover it | Same plan/run, no duplicate apply |
| Runner failed, was cancelled, or heartbeat expired before mutation proof | Inspect state lock and provider plan; use protected `recover` only when provenance still matches | First unproven phase |
| Provider mutation may have happened, but checkpoint is absent or ambiguous | Stop automatic progress; obtain state/inventory evidence and operator review | Fail closed; do not replay unknown side effects |
| Non-destructive remaining diff is proven to be a subset of the reviewed plan | Create a numbered retry through protected recovery | First unproven phase after bound evidence |
| Reviewed pure node contraction remains and every selected node is still present | Use the configured contraction executor with exact-node, remaining-capacity, readiness, and drain proof | Stop if any reviewed node is already absent |
| Same-address replacement, mixed destructive plan, core-network deletion, or post-deletion recovery remains | No current hands-off executor | Fail closed; create a separately reviewed implementation/plan |
| Commit, plan digest, source dispatch, workflow URL, runner identity, or timing classification differs | Do not reuse the attempt | Create/review a new valid plan/dispatch as appropriate |

Recovery is a new runner identity attached to the durable retry lineage; it is
not a way to change intent. The recovery workflow must retain the original
reviewed plan binding and cannot turn `recover` authority into a fresh apply.

## State, drift, and assurance procedure

1. Freeze further applies for the affected plan and inspect active attempts,
   checkpoints, plan receipt, deterministic PR/dispatch, provider state lock,
   and last successful evidence.
2. Compare desired document, plan current/proposed payload, reviewed artifact,
   provider inventory/state, generated inventory, enrollment, Nomad/Consul,
   and service manifest. Record which truth disagrees; do not repair from a
   single dashboard.
3. Classify the discrepancy: no-op observed drift, safe non-destructive
   remainder, unknown provider mutation, or destructive/replacement remainder.
4. For a safe non-destructive remainder, create the bounded recovery attempt,
   checkpoint each proof, and verify assurance after completion.
5. For unknown or destructive state, keep the operation failed/abandoned for
   review. Replan from verified current state; do not hand-edit state or delete
   resources outside the audited process.
6. Verify assurance: required nodes enrolled, Nomad/Consul healthy, placement
   meets intent, old nodes drained when applicable, host assurance passes, and
   user-facing application checks are healthy. App deploys remain separate
   operations and must be checked independently.

## Networking and cleanup boundaries

The Fleet document's networking fields are **target** infrastructure intent.
The live service manifest, Consul instance data, listener/allocation, and
Traefik/cloudflared route are **current** routing truth. Compare both before
declaring networking ready. A container port or created load balancer does not
prove that the user-facing endpoint routes to a healthy allocation.

Disposable fleets require an explicit cleanup and billing contract before
creation: named owner, provider account/project, region, state backend,
expected expiry, resource/tag inventory, final evidence retention, deletion
authority, and cost owner. Cleanup must first drain/stop workloads, retain the
plan/attempt/checkpoint/audit evidence, verify provider resources and billing
tags, then use the reviewed cleanup workflow. Never treat Norn's plan receipt
or an application shutdown as proof that provider billing has stopped.

## Supported and fail-closed destructive work

Supported automated work is limited to validated, reviewed, provenance-bound
non-destructive create/scale-up and the current protected workflow's bounded
pure node-contraction lane. That contraction binds the reviewed plan to exact
existing nodes, proves the remaining declared capacity, reruns
configuration/enrollment/readiness, drains those nodes, and only then applies
the bound deletion. Recovery may resume only while every reviewed node remains
present. A capacity-plan receipt or an `--allow-destructive` acknowledgement is
intent proof; it is not blanket deletion permission.

Same-address size replacement, mixed destructive plans, core-network deletion,
whole-fleet teardown, and recovery after a reviewed node has already
disappeared have no current hands-off executor and fail closed. They require a
separately implemented and reviewed generation/adoption or retirement contract.
