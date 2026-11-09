# Future fresh-Fleet two-app-pool contract

Status: contract under definition. This does not change current
`staging/nyc3`, and it does not authorize a provider plan or apply.

## Boundaries

- Current DO staging uses three control, two ingress, and four app Droplets
  from one `workload: app` pool in protected `norn-fleet` main. Its topology
  source and provider inventory agree, but runtime health is unverified. See
  the [source audit](staging-topology-source-audit-2026-11-08.md).
- A future fresh-Fleet default must declare two distinct application node
  pools. This contract is separate from Genesis, which installs a controller
  on an already-created Fleet, and from app replica scaling.
- The requested future roles are **general** and **stateful**, one per pool.
  This records the operator's choice; it does not imply workload placement,
  VM sizes, or capacity bounds from the current single-app-pool staging source
  or the abstract nine-node recovery model.

## Required intent fields

Each future app-pool record must bind:

| Field | Required evidence / validation |
|---|---|
| Stable pool ID and role | Use the stable IDs `general` and `stateful`, one per role. Roles come from the declared workload labels, never provider node names. |
| Provider size | Exact provider SKU plus a fresh region availability/capability read. |
| Capacity bounds | Integer `min`, `desired`, and `max`; `min <= desired <= max`; approved cost/resource envelope covers the maximum. |
| Placement | Region, VPC/subnet, labels, and separation constraints. Must not permit app workloads onto control nodes unless a separate policy explicitly admits that. |
| Replacement policy | Strategy, surge/headroom, drain timeout, readiness proof, and recovery behavior. |
| Workload contract | Eligible job groups, replica constraints, anti-affinity, statefulness, and any database/network dependencies. |
| Intent freshness | Source commit, config digest, provider account/project identity, region, state backend identity, approval expiry, one-use dispatch nonce. |
| Receipt | Requested and observed count per pool, exact provider node IDs/names/roles, inventory digest, bootstrap/PKI status, workload placement/readiness, and terminal outcome. |

The enclosing Fleet intent also binds control and ingress pools, provider and
state authority, SSH/firewall inputs, generated host identity, secret
provenance, and the exact workflow/artifact. Secret values never appear in
receipts; the receipt records source path/role, digest or public fingerprint,
owner/mode, expiry, and rotation/revocation reference where applicable.

## Current implementation boundary (2026-11-09)

Protected `norn-fleet` main at `35e1c893` supports the General + Stateful role
pair in its topology validator and bootstrap path. Three focused source tests
passed against that exact main revision: `sanity.py` accepts the two-role
topology, generated inventory preserves each pool ID and role, and Ansible
registers separate Nomad pools before clients join. The generated inventory
places both in `norn_role_app` while retaining `norn_fleet_pool` and
`norn_fleet_workload_role`; Nomad uses the real node-pool assignment and a
role-derived node class. The existing protected staging configuration remains
the single `workload: app` pool; no current staging resource was changed.

This is role-pair implementation support, not a ready fresh-Fleet provider
profile or end-to-end provider qualification. No future environment config
has been added or applied. Fresh-Fleet sizing, region/SKU availability, cost
envelope, workload eligibility and anti-affinity, and stateful storage/data
recovery remain open. The deterministic tests exercise validation, generated
inventory and bootstrap composition separately; they do not join those
contracts to a fresh protected apply/recovery, exact provider identity, or a
terminal receipt. Keep the two-pool model as a design contract until that
integrated offline workflow exists.

## Recovery contract

1. Validate both app-pool records and all enclosing inputs before provider
   effects. Unknown, missing, stale, substituted, or out-of-envelope values
   hold the intent.
2. Apply the exact reviewed plan once. A lost response is an unknown effect;
   do not issue a replacement plan or a second create until fresh provider
   readback binds the same account, state, intent, and resource identities.
3. Reconcile each pool independently. Partial creation remains under the same
   intent; create only the missing reviewed capacity after exact readback.
   Unexpected or over-capacity nodes hold for review.
4. Do not produce inventory until every requested pool has exact provider
   identity and requested count. Do not run bootstrap/PKI until the complete
   inventory passes role, address, key, and topology checks.
5. Do not report success until bootstrap, independent postflight, and the
   terminal receipt bind all pools and their workload readiness contracts.
   Recovery resumes the same intent and cannot silently revise sizing.

## Deterministic offline coverage required

The credential-free fixture should consume the same parser/serializer and
input manifest as the protected plan path. It must cover:

- two valid, distinct pool declarations with synthetic sizes and counts;
- missing/duplicate pool IDs, unknown roles, unsupported SKU, invalid
  min/desired/max, and aggregate cost-envelope overflow;
- absent, stale, wrong-account, wrong-project, or substituted backend/provider
  identity and expired/replayed intent;
- partial creation in either pool, lost apply response, retry under the same
  intent, exact per-pool readback, unexpected node, and changed inventory;
- no inventory/bootstrap/receipt from partial or mismatched pools;
- repeated bootstrap after interruption with stable host identity and
  secret-safe receipts;
- per-pool workload placement/readiness and an unavailable pool holding
  terminal success.

The current model treats the general and stateful pools as independent
recovery domains under one immutable Fleet intent and terminal receipt. It
checks role-correct workload placement, general readiness, stateful readiness
and recovery proof, no duplicate provider create, no inventory from partial
state, no configuration before complete identity, no success from incomplete
bootstrap, and same-intent recovery after lost responses. The statechart was
rerun on 2026-11-08: TLC explored 45 states and 98 steps; all 14 invariants
and deadlock checking passed.

This remains an abstract contract model, not qualification of an adopted
topology or implementation. It assumes pool declarations passed validation
and does not model field-by-field input validation, provider SKU/capacity or
cost limits, exact provider node identities, secret transformations,
workload-specific data integrity, or correspondence to the protected apply
workflow. Those require deterministic contract fixtures and implementation
checks before a provider run.

## Open decisions before configuration changes

- Provider SKU and region-specific availability for each pool.
- `min` / `desired` / `max` and total cost/resource budget.
- Workload placement and anti-affinity within the selected general/stateful
  roles, including stateful data and recovery requirements.
- Whether this contract changes only future fresh-Fleet defaults or also
  proposes a reviewed staging migration after current staging release gates.
