# Nine-node staging-topology create and recovery model

This chart models a 3-control + 2-ingress + 4-app Fleet. Those pool counts
match protected `norn-fleet` main at
`6335c667693d0333d4239f91edf2efb9772ab63a` and the 2026-11-09 18:58 UTC
provider inventory. This is topology alignment, not a reproduction of the
current protected input parser/workflow or proof of live bootstrap/recovery.
The model abstracts inputs, provider calls, and node identities. It focuses
on partial create, lost response, exact state/provider readback, and same-intent
forward recovery. Persistent staging has no sanctioned full-Fleet destroy
path, so this model has no cleanup or retirement action. See the [topology
source audit](../../staging-topology-source-audit-2026-11-08.md).

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

This protocol model matches the declared pool counts but is not evidence that
the protected workflow implements each transition. Bind the model to the
actual input parser and replay each path against controller fixtures before
treating it as staging recovery qualification.

## TLC result

TLC explored 102 states and 210 transitions. All ten configured invariants
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

The model was rerun on 2026-11-09 and reproduced 102 states, 210 steps, all
ten invariants, and the deadlock check. The current public canary route was
read separately at 19:01 UTC; neither result qualifies fresh-Fleet bootstrap or
its protected apply/recovery workflow.

The generated chart, TLC report, and `tlc.out` are retained in this directory.
