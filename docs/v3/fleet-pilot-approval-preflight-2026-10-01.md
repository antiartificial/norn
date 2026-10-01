# Disposable paired Fleet pilot approval and precreate boundary

Status at 2026-10-01T03:22:35Z: offline prepared; first billable creation
has not been recorded. This is a sanitized index of owner-only operator
receipts, not a provider apply, billing statement, or Fleet fitness result.

The owner-approved run is `pilot261001a`. Its private
`norn.local-paired-pilot-spend-authorization/v1` receipt was prepared at
`2026-10-01T01:32:49.856978Z` and has SHA-256
`2f2d7ff5509b95043e2c2eb82fdebe70a627da4ba51700c652d7fc130440263d`.
It binds a **$10 incremental ceiling**, **four hours from the first billable
create**, and complete retirement. Its maximum resource envelope is eight
Droplets, one load balancer, three managed database clusters/six database
nodes, and two state buckets. The named scope is paired management and Fleet
fitness, optional ingress 2→3→2, and complete retirement. Approval is for
that envelope; it is not permission to infer provider creation or a successful
scale exercise.

The owner-only `norn.operator-paired-pilot-clock/v1` receipt has SHA-256
`b0f83c63012ac8135505988bf46e6d2eda9c981b5fb55ef31ea1e9506707b06e`.
It records `pending-first-billable-create` and a null first-create timestamp.
The independent provider readback at `2026-10-01T03:22:35.287865Z` recorded
zero mutations for this run. Its account inventory included two unrelated
pre-existing Droplets, zero load balancers and zero managed database clusters;
it did not establish a permanently empty account. The offline readiness
receipt recorded no billable pilot resource at that check. Fresh provider,
state, key, capacity, quote, and clock reconciliation is required immediately
before the first create and after retirement.

Fleet exact-main source `39b7b3c817ae818014455eead12be4991dc39698`
passed protected validation and no-cloud qualification. The protected
ingress 2→3→2 implementation is already in that source through
[Fleet PR #204](https://github.com/antiartificial/norn-fleet/pull/204), but
the pilot proposal still stages a fixed three-control/two-ingress creation.
An ingress scale exercise needs its own reviewed run-bound transition, Norn
plan, protected attempt, exact-node drain, and remaining time/cost checks.
It tests an ingress workload pool; it does not prove a separate application
pool. The first live pilot may stop after the fixed five-node matrix if those
additional gates or the retirement reserve cannot be met.

No Tailscale policy delegation, enrollment keys, Spaces backend/scoped key,
protected runner, state plan, or provider mutation is established by this
receipt. Those precreate gates remain separate. Stop new creates after two
hours, start retirement by three hours, and verify Fleet zero before retiring
management, then verify final provider and billing inventory.
