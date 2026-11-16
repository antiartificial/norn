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

## Milestones and dependencies

| Milestone | Work | Exit proof |
| --- | --- | --- |
| 0. Retire frozen implementation | Remove root Kubernetes API/CLI/UI, old multi-provider k3s provisioning, and their packaging/tests. Forward root make commands to the active implementation. | Current binaries build without those trees; historical docs, active source, release catalog, and verifier hash remain intact. See the [retirement record](../legacy-retirement-2026-11-16.md). |
| 1. Establish the contract boundary | Inventory public APIs, capabilities, InfraSpec, operation/effect receipts, signed artifacts, and compatibility windows. Assign each contract a single owner and versioned fixtures. | Both product consumers accept the real serialized fixtures and reject incompatible inputs. No duplicate independently edited contract copies. |
| 2. Separate runtime composition | Create independent Fleet and self-hosted entrypoints/configuration around the existing domain code. Bind control-store adapters, maintenance executor, and authentication policy explicitly. | Each composition builds and starts with its own supported inputs. Unsupported capabilities are explicit. Existing behavior and recovery tests pass. |
| 3. Separate packaging and qualification | Give each product its own artifact identity, install closure, release workflow, trust settings, and qualification record. | Real installed packages complete admission, effect/readback, restart, and terminal cleanup with their intended store and host roles. |
| 4. Extract self-hosting | Move the self-hosted composition, tests, docs, and required history into its repository. Pin shared contracts/core dependencies. Update repository-bound source checks and publisher App access deliberately. | The extracted checkout builds/tests without sibling paths or local replace directives. No copied operational credentials or machine state are required. |
| 5. Finish the Fleet repository | Move the remaining platform code from the version-named directory into a clear Fleet layout, updating imports, build paths, workflow pins, and source audits together. | Fleet builds and qualifies from its own checkout. Current/next artifact compatibility and same-intent recovery remain proven. Remove old callers and their code/tests in the same change. |

The extraction itself is future work. No new repository, running deployment,
release key, or remote history is changed by this cleanup.

## Source ownership to resolve before extraction

| Current code | Ownership and required decision |
| --- | --- |
| `v2/api/etcdstore`, Fleet runtime entrypoints, `v2/api/fleet` | Fleet control state and infrastructure/controller contracts. Identify shared application/domain dependencies before moving. |
| `v2/api/store` and direct-host maintenance | Self-hosted PostgreSQL control lifecycle. Separate it from application database support before pruning. |
| `v2/api/database` | Application PostgreSQL/MySQL bindings can be required by Fleet too. A Fleet-only control store does not make these adapters obsolete. |
| `v2/api/model`, validation, Nomad/Consul integration, ingress, client contracts | Shared behavior needs one owner. Use a pinned contract bundle or a small versioned module when actual imports require shared code. Do not fork the entire server into two copies. |
| `v2/api/main.go` | Mixed runtime composition. Separate startup and route wiring before extraction, while preserving capability negotiation and auth semantics. |
| `v2/scripts/platform-upgrade` and release helpers | Mixed host roles and shared artifact consumers. Fleet controls also consume release tooling; moving the entire scripts directory to self-hosting would break Fleet. |
| `v2/cli`, `v2/ui` | Choose capability-aware shared clients or product-specific shells around the same contract. Keep target endpoint and credential identity explicit. |
| `v2/infra`, `.github/workflows`, `release/online-compatibility.json` | Assign each fixture and workflow to its supported product. Retain Linux Fleet qualification and independently scope self-hosted tests. Preserve historical compatibility inputs. |
| Root `go.mod` | Active build input to the pinned intent verifier. Its Go directive changes embedded runtime defaults and binary bytes. Retain until a separately reviewed build/pin migration. |

A third shared-runtime repository is not a prerequisite. Start with explicit
entrypoints and versioned contracts. Extract a shared library only when both
products have concrete consumers that justify it.

## Work that can proceed concurrently

After milestone 1 fixes contract ownership, these bounded tasks can proceed in
parallel:

- Inventory and separate runtime entrypoints, stores, and maintenance paths.
- Prepare product-specific package/install manifests and workflow input checks.
- Classify tests and fixtures as shared-contract, Fleet, self-hosted, or retired.
- Update client capability handling and each product's documentation/build guide.

Integrate them before milestone 4. Each task must name its dependencies and
produce executable evidence. Keep one shared-contract test owner and focused
consumer checks rather than copying the complete suite into every repository.
A single operator does not require a new operator-concurrency framework;
existing crash, expiry, and ambiguous-effect recovery remain necessary.

## Compatibility and recovery acceptance

The split is complete only when:

- Each repository builds, tests, and packages from a clean independent checkout.
- Shared wire contracts retain their required auth, idempotency, serialization,
  and capability behavior across current and next consumers.
- Fleet runtime has no dependency on self-hosted control state or host services.
- Self-hosted runtime can operate without Fleet/provider credentials or a quorum.
- Artifact readers, release source pins, GitHub App repository access, and
  install paths are bound to the intended product. A shared API is insufficient
  to establish these release boundaries.
- Each product rehearses its own upgrade/restart/terminal recovery before its
  replacement artifact is deployed. The qualified current artifact remains
  available until that proof passes.
- No stale caller, duplicate domain implementation, or obsolete test remains
  solely to preserve an intermediate extraction layout.

## Retired and retained tests

The cleanup removes nine Go test files and three UI test files with their
frozen implementations. The removed source remains in the immutable
[`v3.0.0` tag](https://github.com/antiartificial/norn/tree/v3.0.0).

The active `v2/` tree contains the V3 implementation. Its `/api/v1` routes,
versioned receipt/effect schemas, PostgreSQL control recovery, and application
database tests still have consumers. Keep them until those behaviors move to
their owning product or are deliberately retired with their callers.

The [earlier caller inventory](../v2-surface-retirement-inventory-2026-11-07.md)
remains historical context, not proof that current consumers have disappeared.
