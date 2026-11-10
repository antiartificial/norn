# Genesis readback source and input gate

This bounded statechart covers the recovery path exposed by DO Genesis
readback run [42418784411](https://github.com/antiartificial/norn-fleet/actions/runs/42418784411):
a fresh one-use intent used an installed verifier source that did not contain
the reviewed capability/target commits, so readback held without changing
Fleet state. The model requires a new intent after the failed one is spent.

The model also covers the current pre-dispatch hold. If the protected readback
gate is disabled or the approved state binding is stale, no intent is staged
and no runner/readback begins. If those gates are open, a fresh one-use intent
is staged against exact state and host-key bindings. A stale installed verifier
then spends that intent and holds; the reviewed installer transition updates
the verifier while Genesis units stay off, after which a new intent can pass
read-only journal readback.

The normal recovery config checks 18 states and 52 transitions. The disabled
gate config checks 2 states and 2 transitions, including the closed-gate hold.
All six configured invariants and deadlock checks pass in both configs. The
normal chart draws eight states and 13 arrows; the disabled chart draws two
states and one arrow. Repeated intent-staging transitions are hidden. The
invariants require current source for verified readback, one-use intent
consumption, a dormant Genesis install, no provider mutation, and no intent or
readback after a closed pre-dispatch gate.

The offline scenarios capture the observed 2026-11-10 conditions: the
configured release-state digest changed from the prior owner-approved digest,
and the protected readback-enable variable is absent. This is a protocol
rehearsal, not fresh DO runtime evidence, and does not qualify the protected
installer or live Genesis readback.

The model treats signatures, state/host-key digests, source installation, and
the controller journal as abstract inputs. It does not prove SSH, provider
state, protected installer execution, owner-key handling, or the deployed
management host's file contents. `InstallReviewedVerifier` represents the
reviewed protected bootstrap action; it is not authorization to dispatch it.

## Reproduce

From the Norn repository root, use new output directories:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGate.tla \
  --config docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGate.cfg \
  --view docs/v3/statecharts/genesis-readback-source-gate/view.ts \
  --out /tmp/norn-genesis-readback-source-gate

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGate.tla \
  --config docs/v3/statecharts/genesis-readback-source-gate/GenesisReadbackGateDisabled.cfg \
  --view docs/v3/statecharts/genesis-readback-source-gate/view.ts \
  --out /tmp/norn-genesis-readback-disabled
```

The checked-in [normal chart](chart.mmd) and [TLC report](report.md) show the
recovery path. The [closed-gate chart](GenesisReadbackGateDisabled.chart.mmd)
and [closed-gate TLC report](GenesisReadbackGateDisabled.report.md) show the
pre-dispatch hold.
