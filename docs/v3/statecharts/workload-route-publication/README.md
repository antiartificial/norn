# Workload route publication and recovery

This bounded model describes one mixed-route step for a stateless workload.
It separates durable intent reservation, per-node route writes, independent
route readback, application/public traffic proof, and one atomic etcd
transaction that compare-and-swaps the active route generation together with
the release-receipt successor.

TLC reached 55 states and 111 steps; eight safety invariants and deadlock
freedom held. The chart draws 12 states and 25 arrows. The active generation
cannot advance without its durable workload receipt.

The checked properties cover:

- A release receipt requires independent route and traffic proof.
- Traffic proof requires the exact route on every ingress member.
- Route writes require the accepted intent, pinned generation, and live
  controller authority.
- Lost-response recovery retains the same generation and route step.
- The active-route and receipt transaction is atomic. A lost response after
  commit triggers same-generation node and traffic revalidation before old-stop
  authorization.
- A node publication is recorded once per member for the reserved step.
- Old workload mutation requires a fresh inventory proof after route receipt;
  inventory drift during the route step holds progress.
- Target health must remain valid through route commit.
- A committed route receipt matches the active route generation.

The model allows a lost response after partial publication and after the
atomic active-route/receipt transaction. Recovery retries or re-reads the same opaque intent and
generation against the same inventory; it does not allocate a replacement
route generation. Inventory, health, competing route writer, or authority
loss holds the protocol. A later inventory change after receipt commit blocks
old-stop authorization; it requires a fresh reviewed continuation. Independent
traffic proof precedes the atomic active-route/receipt transaction, so a node
acknowledgement alone cannot commit progress.

The bounded route protocol was rerun on 2026-11-08 with the repository's
`statechart` skill and produced the same 55-state, 111-step result. These
properties are model evidence. The implementation now has durable workload
route intents, current target and ingress inventory checks, active-route plus
receipt CAS, claim/lock-bound node authority, independent route and traffic
proof, and same-intent route replay. Etcd integration tests cover partial
publication retry and lost commit response. A deterministic driver-level
fixture now injects loss after initialization, health readback, each route
step, stop, drain, and completion, then re-enters the same reconciler. A
production-driver process/lease crash matrix and live qualification remain
open.

Regenerate the chart, report, and TLC output with:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/WorkloadRoutePublication.tla" \
  --config "$PWD/WorkloadRoutePublication.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```
