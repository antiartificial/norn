# V3 capability status

Reviewed 2026-11-16. Norn includes application discovery, preflight, deployment,
service visibility, secrets/dependency bindings, events, and recovery interfaces.
This reference records the first Fleet milestone and its qualification limits.
A passing source test, published artifact, and live-qualified operation are
separate kinds of evidence.

| Capability | Current evidence | Limit |
| --- | --- | --- |
| Signed platform publication | Immutable, signed source `cdf2081e10ed117563aeb3a35d19c0baede1f42d` passed qualification and publication. | Publication alone does not deploy it. |
| Existing-Fleet online upgrade | Live run 43852466595 upgraded three controls from `fa301fa5` to `cdf2081e`, with passive controls first and the active owner last. | Evidence applies to that source pair, topology, and signed policy. It is not a universal zero-downtime guarantee. |
| Workload continuity | The accepted window recorded 399 official observer samples and 673 separate public probes without an observed outage or API handoff gap. | Nine timeout probes occurred later in the longer monitor. Long-duration reliability remains separate. |
| Controller finalization | Success receipt retained, active journal cleared, host authorities retired, worker and observer stopped. | Offline recovery models do not qualify every live fault combination. |
| Pilot retirement | Exact-ID doctl cleanup and final provider readback confirmed nine nodes, two application databases, dedicated load balancer, and Fleet networking absent. | This owner-authorized fallback did not live-qualify the saved-plan retirement controller. |
| Management retention | Two management nodes remain active, their database online, runner reachable over pinned SSH, and authority online on Tailscale. | The readback did not establish management API health or billing settlement. |
| Application hosting | An existing Fleet canary responded through the platform upgrade. | The first durable controller-admitted workload receipt remains unqualified. Hosting and new controller admission are different claims. |
| Fresh Fleet creation | Topology validation, General and Stateful pool roles, models, and default-off controller work exist. | No fresh protected provider creation is live-qualified by this pilot. |
| Workload resizing | Mixed-version models and disposable Nomad recovery checks exist. | No live DO mixed-version resize is qualified. Stateful compatibility requires its own proof. |
| Self-hosted strategy | Single-host operation with PostgreSQL control state, application hosting, and direct host maintenance. | Separate upgrade/recovery lifecycle. Fleet quorum and pool resizing do not apply. |

## Version names are independent

Norn 3.0.0 is the product milestone. `/api/v1` is a current control API
namespace. Effect-grant and receipt schema versions identify their own
contracts. The `v2/` source tree contains the active V3 implementation.
Do not remove these contracts or their tests by matching a version string.

See [release milestones](../release-program-milestones.md) for source links and
[version identity](versioning.md) for artifact and product naming.
