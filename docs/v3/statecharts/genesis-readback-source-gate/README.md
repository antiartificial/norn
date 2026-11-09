# Genesis readback source-version gate

This bounded statechart covers the recovery path exposed by DO Genesis
readback run [42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411):
a fresh one-use intent used an installed verifier source that did not contain
the reviewed capability/target commits, so readback held without changing
Fleet state. The model requires a new intent after the failed one is spent.

The protected bootstrap step installs the reviewed verifier source while the
Genesis units remain off. A fresh intent bound to the verified state and host
keys can then pass the read-only journal readback. The installer transition is
conditional. The protected
`NORN_WORK_PLATFORM_CONTROLLER_BOOTSTRAP_ENABLED` environment gate was
`false` at 19:09 UTC on 2026-11-09. The protected `norn-fleet` main source is
`6335c667693d0333d4239f91edf2efb9772ab63a`; this does not prove that source
is installed on the management host.

TLC checked 18 states and 52 transitions. All five configured invariants and
the deadlock check passed. The chart draws eight states and 13 arrows. It hides
seven repeated intent-staging transitions. The rules require a current source
for verified readback, consume each one-use intent, keep Genesis units
inactive, and forbid provider mutation in this flow.

The model was rerun on 2026-11-09 and reproduced 18 states, 52 steps, all five
invariants, and the deadlock check. The bounded protocol result does not qualify
the protected installer or live Genesis readback.

The model treats signatures, state/host-key digests, source installation, and
the controller journal as abstract inputs. It does not prove SSH, provider
state, protected installer execution, owner-key handling, or the deployed
management host's file contents. `InstallReviewedVerifier` represents the
reviewed protected bootstrap action; it is not authorization to dispatch it.

## Reproduce

From the Norn repository root, use a new output directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGate.tla \
  --config docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGate.cfg \
  --view docs/v3/statecharts/genesis-readback-source-gate/view.ts \
  --out /tmp/norn-genesis-readback-source-gate
```

The generated [chart source](https://github.com/antiartificial/norn/blob/877ad4ba/docs/v3/statecharts/genesis-readback-source-gate/chart.mmd) and [TLC report](report.md) show the checked
transition graph and result.
