# Norn V3 lane statecharts

These bounded TLA+ checks are offline models. A passing model checks the
specified state transitions and invariants; it does not prove live credentials,
provider state, host behavior, availability, or the implementation itself.

| Lane | Result | Model status | Main limit |
|---|---:|---|---|
| Existing DO platform release | 299 states, 1,036 steps; 10 invariants pass | Existing all-stop, forward-only release model | Does not cover Genesis installation or fresh provider creation |
| Mini direct V3 upgrade | 82 states, 164 steps; 7 invariants and deadlock check pass | Bounded model of backup/shadow, writer proof, fencing, migration, promotion, receipt, and same-target recovery | Abstracts real key custody, writer discovery, Launchd PID reuse, and host effects |
| Existing Fleet Genesis | 69 states, 107 steps; 9 invariants and deadlock check pass | Dormant install, signed intent, staged controls, passive-first promotion, recovery; fresh-Fleet request is explicitly blocked | Inputs are modeled as one valid/invalid gate; provider create workflow is absent from this model |
| Workload mixed-version resize | 102 states, 279 steps; 10 invariants and deadlock check pass | Proposed protocol, not current controller behavior | Abstracts Nomad, app processes, database semantics, route implementation, and timing |
| Fresh DO Fleet create and recovery | 308 states, 813 steps; 8 invariants and deadlock check pass | Bounded protected plan/apply, partial create, readback, same-intent recovery, PKI/configuration, exact cleanup | Three abstract node groups; detailed provider resources and secret lifecycle are omitted |
| Future fresh Fleet with two app pools | 45 states, 98 steps; 14 invariants and deadlock check pass | General + stateful pool create/recovery, role-correct workload placement/readiness, stateful recovery proof, then shared inventory/configuration/receipt | Abstract counts/identities, provider API, secret lifecycle, workload-specific data integrity, and implementation behavior |
| Nine-node staging-topology create and recovery | 102 states, 210 steps; 10 invariants and deadlock check pass | 3+2+4 partial-create, fresh exact readback, same-intent forward recovery, then inventory/PKI/configuration; pool counts match protected `norn-fleet` main | Not wired to the protected provider workflow; node identities abstracted, inputs/credentials aggregate, no provider, GitHub Actions, SSH, Ansible, or teardown behavior |
| Workload old-generation stop recovery | 10 states, 15 steps; 7 invariants and deadlock check pass | Bounded target-health and mixed-route gate, stop accepted/lost response, marker recovery, old allocation drain | Does not model effect-store crashes, signed admission, API dispatch, durable receipt loading, route publication, or live Nomad |
| Workload route publication and recovery | 55 states, 111 steps; 8 invariants and deadlock check pass | Per-node mixed-route publication; atomic active-route/receipt CAS; same-generation recovery and fresh pre-stop route/inventory proof | Does not prove live workload authority, inventory source, route CAS store, ingress transport, or controller implementation |

## Model files

- [Existing DO platform release](fleet-platform-release/README.md)
- [Mini direct upgrade](mini-direct/README.md)
- [Genesis on existing DO controls](genesis-existing-fleet/README.md)
- [Proposed workload mixed-version resize](workload-mixed-version/README.md)
- [Fresh DO Fleet create and recovery](fresh-fleet-create/README.md)
- [Future fresh Fleet with two app pools](fresh-fleet-two-app-pools/README.md)
- [Nine-node staging-topology create and recovery](fresh-staging-nine-node-recovery/README.md)
- [Workload old-generation stop recovery](workload-stop-recovery/README.md)
- [Workload route publication and recovery](workload-route-publication/README.md)

## Findings to carry into implementation

- The Mini model's writer gate follows protected backup and signed shadow
  preparation, and precedes journal binding and live host effects.
- The workload proposal stops effects while held, waits for healthy target
  capacity before draining old allocations, and requires fresh proof of the
  exact current route after recovery. Route publications move only from old to
  mixed to target; switching back requires a separately admitted intent. Loss
  of one canary allocation invalidates route proof and holds old-version drain
  until fresh readback confirms the route and same-intent recovery reconciles.
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
- No online mixed-version Norn control upgrade is modeled or qualified yet.

Each lane directory contains the TLA+ spec, TLC config, chart view, Mermaid
chart, report, run output, and reproduction notes. Regenerate with the command
in that lane's `README.md`.
