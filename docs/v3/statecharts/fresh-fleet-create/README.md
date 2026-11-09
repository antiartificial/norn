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
