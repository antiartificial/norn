# Workload mixed-version model

This is a bounded **protocol model**, not a model of currently deployed mixed-version behavior. The repository has an internal M5 controller candidate with signed acceptance, durable receipts, route proof, and stop recovery, but the candidate is not merged, operator-admitted, or live-qualified. This model checks protocol ordering, including target-only route proof before the old cohort can drain.

Scope: two old allocations, two target allocations, three total surge slots, one workload, one additive schema expansion, and one controller. It abstracts each allocation as a writer-bearing workload unit. It does not represent separate web/worker processes, real Nomad placement, cloud eventual consistency, route implementation, database semantics, timing, or credential custody.

Generate/check with:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadMixedVersion.tla" \
  --config "$PWD/WorkloadMixedVersion.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

Run the command from this directory.

Latest deterministic run: TLC reached 24 states and 52 steps. The chart shows 9 states and 11 arrows; its view hides 34 steps. It includes the release ordering rule that old allocations may drain only after a target-only route is published and proved. It exposes separate stale-route
holds and the fresh readback that confirms the exact current route before
effects resume. It also includes loss of one canary allocation while the mixed
route is serving: that invalidates route proof and holds old-version drain.
The compact view hides repeated crashes from later phases, route-proof-loss
cases outside the canary phase, and recovery reconciliations that rejoin later
main-flow states; TLC still checks them all. TLC found no invariant violations
and checked `NoDeadlock`. Invariants cover surge capacity, allocation bounds,
ready allocation counts, route readiness, drain ordering, compatibility during
overlap, schema contraction after old writers exit, terminal receipt
correctness, and explicit holds. This is a proof of the bounded proposed
transition system only, not proof that the current controller implements
mixed-version releases.

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
