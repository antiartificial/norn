# Norn V3 lane statecharts

These bounded TLA+ checks are offline models. A passing model checks the
specified state transitions and invariants; it does not prove live credentials,
provider state, host behavior, availability, or the implementation itself.

On 2026-11-09, all 21 checked-in TLC configurations were rerun against Norn
master `19297662` using the statechart skill. Every configured rule passed.
Configurations with `NoDeadlock` enabled passed deadlock checking; the
concurrent-admission config deliberately disables deadlock checking because
terminal result states stutter. This includes the four missing-evidence
admission cases (each 4 states / 6 steps) and the two-client concurrent store
case (14 states / 18 steps), as well as the primary lane models listed below.
Outputs were written under `/tmp/norn-*-check`; no generated outputs or
provider effects were added to the repository. The statechart reports record
the bounds and omissions for each model.

On 2026-11-10, ten focused TLC configurations across eight protocols were
rerun against the current tree: Mini recovery (90 states / 179 steps), workload
capacity four (103 / 275) and three (24 / 52), future General + Stateful pools
(45 / 98), nine-node staging recovery (102 / 210), existing DO release
(299 / 1,036), proposed online Norn upgrade (97 / 251), existing-Fleet Genesis
(69 / 107), and the Genesis source-readback gate in normal (18 / 52) and
disabled/stale-binding (2 / 2) configurations. All configured invariants and
deadlock checks passed. Outputs stayed under `/tmp`; no provider or host action
occurred. This focused pass does not replace a complete 21-configuration
rerun when model sources or shared semantics change.

| Lane | Result | Model status | Main limit |
|---|---:|---|---|
| Existing DO platform release | 299 states, 1,036 steps; 10 invariants pass (rerun 2026-11-10) | Existing all-stop, forward-only release model | Does not cover Genesis installation or fresh provider creation |
| Mini direct V3 upgrade and recovered-forward repair | 90 states, 179 steps; 8 invariants and deadlock check pass (rerun 2026-11-10) | Includes the observed host-agent-fenced halt; ordinary same-intent retry is blocked until a distinct signed repair-controller action verifies lineage and resumes the exact generation | The repair action abstracts signature, source identity, lineage verification, Launchd, SQL/filesystem durability, timing, and key custody; not yet refined to implementation tests |
| Proposed online Norn platform upgrade | 97 states, 251 steps; 12 invariants and deadlock check pass (rerun 2026-11-10) | Old release serves through additive schema expansion and passive candidate verification; writer/traffic handoff is fenced, crash recovery keeps the same intent, and schema contraction waits until old-writer retirement | Proposed protocol only; assumes binary/schema compatibility and post-handoff health; abstracts real migrations, writer discovery, fencing, supervision, workload/data integrity, and availability measurements |
| Existing Fleet Genesis | 69 states, 107 steps; 9 invariants and deadlock check pass (rerun 2026-11-10) | Dormant install, signed intent, staged controls, passive-first promotion, recovery; fresh-Fleet request is explicitly blocked | Inputs are modeled as one valid/invalid gate; provider create workflow is absent from this model |
| DO Genesis readback source and input gate | Normal recovery: 18 states / 52 steps; closed/stale gate: 2 / 2; 6 invariants and deadlock check pass in both (rerun 2026-11-10) | Models closed protected workflow or stale exact state binding holding before intent/runner dispatch; normal recovery requires a fresh one-use intent after source update | Digests, signatures, protected installer, journal, and provider/runtime are abstract; no live dispatch or Genesis state change |
| Workload mixed-version resize | Capacity 3: 24 states / 52 steps; capacity 4: 103 / 275; 11 invariants and deadlock check pass in both (rerun 2026-11-10) | Proposed protocol, not current controller behavior; capacity 3 has no target-only route or terminal path under the target-only-before-drain rule, while capacity 4 reaches terminal receipt | Abstracts Nomad placement, app processes, database semantics, route implementation, and timing; live preflight must prove full overlap capacity |
| Fresh DO Fleet create and recovery | 308 states, 813 steps; 8 invariants and deadlock check pass | Bounded protected plan/apply, partial create, readback, same-intent recovery, PKI/configuration, exact cleanup | Three abstract node groups; detailed provider resources and secret lifecycle are omitted |
| Future fresh Fleet with two app pools | 45 states, 98 steps; 14 invariants and deadlock check pass (rerun 2026-11-10) | General + stateful pool create/recovery, role-correct workload placement/readiness, stateful recovery proof, then shared inventory/configuration/receipt | Abstract counts/identities, provider API, secret lifecycle, workload-specific data integrity, and implementation behavior |
| Nine-node staging-topology create and recovery | 102 states, 210 steps; 10 invariants and deadlock check pass (rerun 2026-11-10) | 3+2+4 partial-create, fresh exact readback, same-intent forward recovery, then inventory/PKI/configuration; pool counts match protected `norn-fleet` main | Not wired to the protected provider workflow; node identities abstracted, inputs/credentials aggregate, no provider, GitHub Actions, SSH, Ansible, or teardown behavior |
| Workload old-generation stop recovery | 10 states, 15 steps; 7 invariants and deadlock check pass | Bounded target-health and mixed-route gate, stop accepted/lost response, marker recovery, old allocation drain | Does not model effect-store crashes, signed admission, API dispatch, durable receipt loading, route publication, or live Nomad |
| Workload route publication and recovery | 55 states, 111 steps; 8 invariants and deadlock check pass | Per-node mixed-route publication; atomic active-route/receipt CAS; same-generation recovery and fresh pre-stop route/inventory proof | Does not prove live workload authority, inventory source, route CAS store, ingress transport, or controller implementation |
| Concurrent workload admission replay | 14 states, 18 transitions; 3 invariants pass | Two independent etcd clients contend for a stable request; the fence loser rechecks durable identity and returns the winner | Store-level only; does not exercise HTTP handling or process restart |
| Workload admission replay after response loss and restart | 115 states, 365 steps; 10 invariants and deadlock check pass | Durable acceptance with a lost response, controller stop/restart without local acceptance memory, and exact same-key replay | Abstract protocol model; does not exercise the HTTP handler, etcd adapter, or runtime worker/controller |
| Simultaneous workload release HTTP admission | 19 states, 28 steps; 6 invariants and deadlock check pass | Two handlers miss initial replay lookup, read current evidence, then converge at atomic acceptance | HTTP test uses a deterministic store fake; the independent etcd test covers the real store boundary, but no combined HTTP-to-etcd test yet |

## Model files

- [Existing DO platform release](fleet-platform-release/README.md)
- [Mini direct upgrade](mini-direct/README.md)
- [Proposed online Norn platform upgrade](online-norn-upgrade/README.md)
- [Genesis on existing DO controls](genesis-existing-fleet/README.md)
- [Proposed workload mixed-version resize](workload-mixed-version/README.md)
- [Fresh DO Fleet create and recovery](fresh-fleet-create/README.md)
- [Future fresh Fleet with two app pools](fresh-fleet-two-app-pools/README.md)
- [Nine-node staging-topology create and recovery](fresh-staging-nine-node-recovery/README.md)
- [Workload old-generation stop recovery](workload-stop-recovery/README.md)
- [Workload route publication and recovery](workload-route-publication/README.md)
- [Concurrent workload admission replay](workload-release-concurrent-admission/README.md)
- [Workload admission replay after response loss and restart](workload-release-admission/README.md)
- [Simultaneous workload release HTTP admission](workload-release-http-concurrency/README.md)

## Findings to carry into implementation

- The Mini model's writer gate follows protected backup and signed shadow
  preparation, and precedes journal binding and live host effects.
- The Mini recovery model now represents the actual halt after host-agent
  fencing: the API remains on the old source, before write fencing or schema
  migration. Normal retry cannot resume that phase; the modeled repair action
  requires the exact journal target and preserved observed state. Its signature
  and source-lineage checks remain an implementation-refinement task.
- The workload proposal stops effects while held, waits for healthy target
  capacity before draining old allocations, and requires fresh proof of the
  exact current route after recovery. Route publications move only from old to
  mixed to target; switching back requires a separately admitted intent. Loss
  of one canary allocation invalidates route proof and holds old-version drain
  until fresh readback confirms the route and same-intent recovery reconciles.
- In the mixed-version workload model, three simultaneous allocation slots
  preserve safety but cannot complete the two-old/two-target rollout without
  weakening the route/drain fence. Four slots provide the modeled full-overlap
  path. Treat this as a preflighted scheduler-capacity requirement, not a
  reason to drain old allocations before target-only route proof.
- The workload route-publication model keeps partial node writes under the same
  intent and generation after a lost response. Inventory drift, target-health
  loss, or authority loss holds progress. An old-stop authorization needs fresh
  inventory and active-route checks even after a historical route receipt was
  committed.
- Genesis covers an existing Fleet; the fresh-Fleet model is separate. The
  provider workflow's integrated input/credential tests, DO token permission
  scope, and exact live cleanup remain open despite the passing bounded model.
- The nine-node model's 3+2+4 pool counts match protected `norn-fleet` main
  and the provider inventory, but its aggregate inputs do not prove the exact
  source parser, protected workflow, or persistent staging recovery. It assumes
  provider-scope evidence at admission even though current preflight does not
  validate token scope; it has no teardown action because persistent staging
  has no sanctioned whole-Fleet destroy.
- The online Norn upgrade now has a bounded proposed model, but no controller
  implementation or live online-upgrade qualification is established.

Each lane directory contains the TLA+ spec, TLC config, chart view, Mermaid
chart, report, run output, and reproduction notes. Regenerate with the command
in that lane's `README.md`.
