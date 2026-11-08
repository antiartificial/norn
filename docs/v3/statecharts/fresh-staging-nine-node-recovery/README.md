# Proposed nine-node Fleet create and recovery model

This chart models a proposed 3-control + 2-ingress + 4-app Fleet, not the
current checked-in `staging/nyc3` source. That source declares 3 control and 2
ingress nodes, no application node pools, and separate managed databases; the
current source has five base compute nodes. See the [topology source audit](../../staging-topology-source-audit-2026-11-08.md).
The chart focuses on
provider partial-create, lost response, exact state/provider readback, and
same-intent forward recovery. Persistent staging has no sanctioned full-Fleet
destroy path, so this model has no cleanup or retirement action.

The aggregate `inputsValid` gate represents the saved plan and digest, capacity
intent and one-use nonce, provider account identity, backend bucket/key/versioning,
SSH/admin route and host-key binding, bootstrap secret bundle, and provider
permission-scope evidence. The model then represents owner-only bootstrap
secret materialization before intent admission. Token-scope evidence is an
intended admission requirement; the current preflight only proves account
identity and backend access, so this condition is not yet implemented.
Generated runtime identities and the per-file provenance, scope, expiry,
rotation, and revocation lifecycle are outside the model.

The node count abstracts the fixed 3+2+4 pool distribution and provider IDs.
`providerMismatch` represents provider/state disagreement or an unclassified
resource; exact readback clears uncertainty only when IDs and backend state
match. It does not model live DigitalOcean APIs, Terraform resource-level
dependency order, SSH, Ansible, PKI bytes, secret values, GitHub Actions
artifact/environment behavior, or Norn's actual receipt APIs.

This is a proposed protocol model for a topology that is not currently
declared by the staging source. It is not evidence that a protected workflow
implements each transition. Adopt a matching input contract, then replay each
path against controller fixtures before treating this as qualification of a
future Fleet.

## TLC result

TLC explored 102 states and 210 transitions. All nine configured invariants
and the deadlock check passed. Invalid/unadmitted input and unresolved identity
mismatch are explicit safe hold states; other holds can read back and recover
only when identity matches. The reader-facing chart groups resource-level
repetitions into 17 states and 35 arrows, including owner-only secret
materialization, partial provider create, mismatch hold, fresh exact readback,
same-intent recovery, inventory/PKI, partial configuration, and terminal
receipt.

Recreate the model with:

```sh
bun "$HOME/.agents/skills/statechart/scripts/statechart.ts" \
  docs/v3/statecharts/fresh-staging-nine-node-recovery/FreshStagingRecovery.tla \
  --config docs/v3/statecharts/fresh-staging-nine-node-recovery/FreshStagingRecovery.cfg \
  --view docs/v3/statecharts/fresh-staging-nine-node-recovery/view.ts \
  --out /tmp/fresh-staging-nine-node-recovery-model
```

The generated chart, TLC report, and `tlc.out` are retained in this directory.
