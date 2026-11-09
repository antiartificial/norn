# Workload mixed-version model

This is a bounded **protocol model**, not a model of currently deployed mixed-version behavior. The repository has an internal M5 controller candidate with signed acceptance, durable receipts, route proof, and stop recovery, but the candidate is not merged, operator-admitted, or live-qualified. This model checks protocol ordering, including target-only route proof before the old cohort can drain.

Scope: two old allocations, two target allocations, one workload, one additive schema expansion, and one controller. Each allocation is a writer-bearing workload unit. The model runs with a three-allocation cap and a four-allocation cap. It does not represent separate web/worker processes, real Nomad placement, cloud eventual consistency, route implementation, database semantics, timing, or credential custody.

Generate/check with:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadMixedVersion.tla" \
  --config "$PWD/WorkloadMixedVersion.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-capacity4"

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadMixedVersion.tla" \
  --config "$PWD/WorkloadMixedVersion.capacity3.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-capacity3"
```

Run the command from this directory.

The default four-allocation run reaches 103 states and 275 steps. All 11
invariants and `NoDeadlock` hold; the compact chart shows 14 states and 19
arrows, including target-only routing, old-cohort drain, writer quiescence, and
terminal receipt. The model treats capacity as simultaneous allocation slots,
not node count. For the two-old/two-target case, live preflight must prove the
target cohort can become fully healthy alongside the serving cohort under
actual Nomad placement and resource constraints.

The three-allocation run reaches 24 states and 52 steps. All 11 invariants and
`NoDeadlock` hold, but the graph has no target-only route, old-cohort drain, or
terminal receipt. This is a progress gap rather than an invariant failure:
with two old and two target allocations, capacity 3 cannot reach the required
fully healthy target cohort while retaining the old cohort. The controller's
target-only route fence correctly prevents draining old allocations to make
room. Do not read a green invariant report as proof that this capacity can
complete a release.

The checked-in [`chart.mmd`](chart.mmd) and [`report.md`](report.md) are the
four-allocation run. [`chart-capacity3.mmd`](chart-capacity3.mmd) and
[`report-capacity3.md`](report-capacity3.md) preserve the insufficient-capacity
case for comparison.

The compact view groups route-proof loss into one held state and hides repeated
crashes from later phases, route-proof-loss cases outside the canary phase, and
recovery reconciliations that rejoin later main-flow states; TLC still checks
them all. Invariants cover capacity, allocation bounds, ready allocation
counts, route readiness, drain ordering, compatibility during overlap, schema
contraction after old writers exit, terminal receipt correctness, and explicit
holds. These are bounded proposed-transition results, not proof that the
current controller implements mixed-version releases.

During model refinement, TLC exposed two unsafe paths in the draft contract:
recovery could continue old-writer quiescence while held, and the old cohort
could be drained twice before the second target allocation was healthy. The
final model blocks effects while held and requires enough healthy target
capacity before each drain. These are requirements to encode in any future
controller, not observed production defects.

Route policy: route publications move forward from old to mixed to target. Loss
of route proof holds the intent and prevents drains or schema contraction.
Same-intent recovery may resume only after a fresh readback confirms the exact
currently published generation; a held route-loss intent cannot resume while
that receipt is stale. A canary allocation loss is represented as loss of route
proof; recovery confirms the still-published mixed route while at least one
target and one old allocation remain healthy. It does not switch traffic back
to the older generation. A rollback route change requires a separately
admitted intent and fresh proof that the older workload remains healthy and
data-compatible.


The implementation review found and fixed a stop-ordering defect: a mixed route had been allowed to enter draining, which could stop old capacity while the route still sent traffic to it. The controller now keeps every mixed step in `Routing`; only the final 0/100 target-only proof enters `Draining`. The model encodes this as `NoOldRetirementBeforeTargetOnlyRoute`. This proves the bounded ordering contract, while the focused Go tests replay receipt admission and stop prerequisites. The candidate remains unmerged and lacks target-specific public traffic evidence and live qualification.
