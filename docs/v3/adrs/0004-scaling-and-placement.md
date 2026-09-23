# ADR 0004: Durable scaling and explicit placement

Status: proposed, 2026-09-22. Planning only; no provisioning or implementation authorized.

## Context

The [v3 roadmap](../architecture-roadmap.md#p3--fleet-capacity-and-rolling-availability) identifies separate gaps in pool enrollment, persistent replica counts and node replacement. More VMs supply capacity and failure headroom; they do not automatically create replicas or redistribute existing allocations. Current bootstrap assumptions also do not implement arbitrary control/app/worker pool names.

## Recommended decision

Represent VM desired capacity and process desired replicas as independent, durable resources. Assign explicit roles to pools independently of pool names, and reconcile provider tags, inventory, Nomad enrollment and ingress membership against those roles. Persist scale changes so a later deployment preserves the accepted desired count. Define explicit precedence between declarative configuration and a scale operation before exposing both writers.

Support per-process resources, pool selection, required distinct-host placement, graceful shutdown and scheduling priority. Use separate Nomad jobs where different pools require it; preserve stable process identities across translation changes. Present pending replicas and unmet placement constraints explicitly. Rebalancing is a reviewed operation, not an implied side effect of adding nodes.

Use generation-based node replacement: provision surge capacity, join and validate, drain the old generation, then retire exact old identities. Admission checks include quota, placement simulation, volume constraints and rollout reserve. Begin with operator-requested scaling; autonomous cloud scaling is outside initial v3 scope.

## Alternatives and tradeoffs

- Imperative scheduler-only scaling is simpler but loses intent during redeployment or recovery.
- Soft host spreading improves utilization but cannot promise host-failure isolation. Permit it only as an explicit workload policy; strict placement can leave replicas pending when too few eligible hosts remain.
- A single combined app/ingress pool lowers cost; separate ingress isolates connection handling and maintenance. Both require explicit roles and matching resource reservations.

## Invariants and failure behavior

- A deployment cannot silently reset an accepted desired replica count.
- Pool intent is accepted only when the installed executor can configure it; unsupported roles fail validation.
- A failed readiness, capacity or drain check stops retirement and leaves the old generation available where possible.
- Equal-node N-1 arithmetic is only an initial budget; admission must also account for fragmentation, constraints and stateful storage.
- Workers require acknowledged-work recovery and idempotency. Stateful web replicas require a qualified shared/external storage and session policy.
- Control membership changes preserve each consensus group's quorum; app replacement cannot authorize control retirement.

## Migration

Import current declared counts and explicit pool mappings into versioned desired state, and surface any divergence from live counts for review. Preserve existing job IDs where possible; otherwise publish an identity map and stage traffic handoff. Roll out role validation, executor support and clients in a compatible order before enabling new pool intent.

## Acceptance evidence

Record a 2→3→2 app-node rehearsal under representative traffic, independent replica scaling, redeployment after scaling, node failure, constrained placement and interrupted replacement. Prove desired counts survive restart, old nodes are retired only after drain, and acknowledged jobs survive worker drain. Report request errors and latency against agreed budgets rather than treating successful provisioning as availability proof.

## Unresolved decisions

Choose scale/config precedence, default placement policy by workload class, surge quota policy, drain deadlines and numerical request/job availability budgets. Settle per-process job identity migration before implementing the translator. These choices block their respective public contracts, not read-only capacity modeling.
