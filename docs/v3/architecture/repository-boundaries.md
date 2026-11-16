# Fleet and self-hosted repository boundaries

The Fleet and independent self-hosted node have different lifecycle needs.
Fleet needs external management, quorum-aware control updates, provider
inventory, and node-pool operations. Self-hosting needs local host integration,
PostgreSQL control state, and recovery for a single machine.

They currently share the implementation under `norn/v2`. The private
`norn-fleet` repository already owns infrastructure configuration and runners.
No product-code split has been executed.

## Proposed ownership

| Repository | Responsibility | Excluded responsibility |
| --- | --- | --- |
| Fleet platform, evolved from `norn` | Fleet API/runtime, etcd control store, Linux release artifacts, application contracts, Fleet clients, and qualification | Mac launch agents, single-host repair and local PostgreSQL control lifecycle |
| Self-hosted product, extracted separately | Independent-node runtime, Mac integration, local PostgreSQL control lifecycle, direct upgrades, and its tests/docs | Provider apply, Fleet quorum, and pool resizing |
| Existing private `norn-fleet` | Provider resources, protected executors, inventory/bootstrap, desired infrastructure, and infrastructure recovery evidence | A duplicate implementation of the platform API or domain rules |

This keeps provider state and secrets in their existing private repository.
Moving all private infrastructure into the public platform repository would
be a separate visibility and ownership decision.

## Share a contract, not release state

Keep the versioned OpenAPI contract, InfraSpec semantics, operation/receipt
schemas, and capability negotiation consistent where both products implement
them. Generate clients from a pinned contract where practical. Each product
advertises supported capabilities and owns its release cadence, keys, state,
migrations, and runtime tests.

An API contract is necessary but not sufficient. Signed artifact formats,
upgrade compatibility, credential exchange, persistence rules, and recovery
receipts also cross repository boundaries. Test those producer/consumer pairs
using the actual serialized output.

## Extraction sequence

1. Inventory files, imports, build inputs, workflow paths, API callers, and
   test ownership. Classify each as Fleet, self-hosted, shared contract, or
   historical implementation.
2. Establish independent build/test entrypoints and one qualification record
   for each product. Preserve the currently qualified artifact and source.
3. Extract self-hosted code with history and its own release trust. Keep the
   existing self-hosted installation unchanged until its replacement artifact is qualified.
4. Move Fleet source to a clear root layout and update all imports, workflows,
   packaging paths, source audits, and generated clients in the same change.
5. Delete dead implementations and duplicate tests after their callers have
   moved. Keep contract and recovery regressions for supported boundaries.

The frozen root Kubernetes implementation is a cleanup candidate. The active
`v2/api`, `v2/cli`, and `v2/ui` directories are not. Tests that protect an
API named `v1`, a receipt named `v2`, or migration from a supported baseline
remain useful until those contracts are deliberately retired.

The split is a proposed source reorganization, not a prerequisite for the
completed online-upgrade milestone. Commits can establish these boundaries
without publishing repeated platform releases.

## Legacy inventory for cleanup

The root `api/`, `cli/`, and `ui/` trees retain the older implementation.
The root API includes Kubernetes code, and the root Makefile still invokes
those trees. In contrast, current repository CI and signed platform publication
build from `v2/`. This makes the root implementation a separate extraction or
retirement candidate, not a reason to delete active V3 tests.

The initial test inventory finds nine root API Go test files and three root UI
test files. Reproduce the list from the repository root:

```sh
git ls-files api cli ui | rg '(_test\.go|\.test\.|\.spec\.)'
```

Deleting those tests alone leaves their old implementation and build entrypoints
behind. Retire or archive that implementation as a coherent change after the
repository ownership decision. Keep V1 and V2 documentation available throughout.

The active `v2/` test suites cover both product behavior and compatibility used
by current clients, release controllers, or recovery paths. Assign them to the
Fleet or self-hosted build when extracting code. Remove a test only when its
behavior has been removed or equivalent behavioral coverage remains in the
owning product. Keep [the earlier caller inventory](../v2-surface-retirement-inventory-2026-11-07.md)
as historical audit context, not proof that current callers have disappeared.
