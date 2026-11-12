# Fresh DO Fleet create and recovery model

This bounded statechart models the existing protected provider plan/apply lane
for a new DO staging Fleet. It separates that lane from Genesis, which installs
a dormant controller on an existing Fleet. No provider, SSH, signing, or
Ansible operation is performed.

## Reproduce

Run from this directory and choose a new output folder:

```sh
JAVA=/Users/arti/.local/share/mise/installs/java/temurin-25.0.5+8.0.LTS/bin/java \
  bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/FreshFleetCreate.tla" \
  --config "$PWD/FreshFleetCreate.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

TLC explored 308 states and 813 transitions. Nine safety invariants and the
deadlock check passed. The chart draws 11 states and 16 arrows. It coarsens
per-group create/configure increments; TLC still checks those branches.

## Modeled contract

- One protected plan digest, capacity plan UUID, and one-use dispatch nonce
  bind to an intent before provider effects.
- Provider account and exact state backend, SSH inputs, and the protected
  runner are separate admission gates.
- Provider effects create abstract control, ingress, and app resource groups.
  A lost create response may leave an ambiguous effect; recovery requires
  provider readback before same-intent resume or cleanup.
- PKI generation and node configuration follow provider creation and state
  reconciliation. Completion requires all three abstract groups configured.
- Partial retirement requires explicit cleanup approval plus exact readback;
  cleanup never acts on unknown identity.

## Limits

This is a proposed bounded contract, not proof of workflow implementation. It
models each node pool as one abstract group rather than nine exact droplets or
all Terraform resources. Input validity is Boolean; individual secret paths,
DO token permission scope, quotas, SSH fingerprints, OIDC, writer fence, and
rotation/revocation details are not modeled. It omits eventual consistency,
timeouts, provider locks, costs, DNS/TLS, management peering, credential values,
Ansible internals, and cleanup deadlines. The model's successful check does not
authorize a live create or prove that a live account token is least privilege.

## Exact nine-node create and recovery contract (2026-11-12)

The companion `FreshFleetNineNodeRecovery.tla` model names the nine
control/ingress/app nodes (3 + 2 + 4) individually. It binds one reviewed plan
and state lineage to one intent and one-use apply. The normal recovery branch
loses the response after eight nodes, retires the apply runner, restarts the
controller, reads back exact provider and state identities, binds a fresh
recovery authorization to the same intent and exact remainder, creates the
ninth node, proves all nine identities, bootstraps, retires the runner, and
writes a terminal receipt. A second branch loses the final state write; the
provider/state mismatch remains held and cannot enter recovery or bootstrap.

Reproduce the companion model in a new output directory:

```sh
JAVA=/Users/arti/.local/share/mise/installs/java/temurin-25.0.5+8.0.LTS/bin/java \
  bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/FreshFleetNineNodeRecovery.tla" \
  --config "$PWD/FreshFleetNineNodeRecovery.cfg" \
  --view "$PWD/nine-node-view.ts" \
  --out "$PWD/nine-node-reproduced"
```

TLC explored 39 states and 58 steps. Ten invariants and the deadlock check
passed. The chart draws 19 states and 23 arrows; it collapses per-node create
and configure loops and repeated input actions, which TLC still explores. See
[`nine-node-chart.mmd`](nine-node-chart.mmd) and
[`nine-node-report.md`](nine-node-report.md).

This is an offline protocol contract, not evidence that the protected workflow
implements the sequence. It models a single app pool of four nodes, excludes
the two management nodes from the nine-node plan, and does not select the
future General + Stateful two-app-pool profile. Provider/state identities and
failure timing are synthetic; real provider apply/recovery, credentials, SSH,
Ansible, controller restart, terminal artifact replay, and live workload
readiness still need implementation-facing qualification. The mismatch branch
fails closed but does not model how a reviewed reconciliation resolves it.

## Norn pre-registration authority gap

The Norn pre-registration authority slice now records the create intent,
protected dispatch binding, attempt, verifier admission, grant, recovery
evidence, terminal receipt, and atomic create-fence-to-target-fence handoff.
Default-off routes and real-etcd tests cover those records, including lost
responses. This remains separate from the existing registered-target executor
and app-delete co-permit: no provider executor consumes the Fresh-Fleet grant,
and the protected workflow does not yet drive this lifecycle end to end. See
the Norn-specific [pre-registration authority model](../fresh-fleet-norn-authority/README.md)
for its trust limits and test evidence. Fresh-Fleet provider effects remain
disabled until provider-executor and protected-workflow integration is
implemented and qualified.
