# Existing Fleet Norn platform release model

Bounded TLA+ model of the existing DO staging Fleet's all-stop, forward-only
release lane. It does not model Genesis install or fresh provider creation.

## Reproduce

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/NornRelease.tla" \
  --config "$PWD/NornRelease.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

Run from this directory. The current retained TLC output reports 299 states,
1,036 steps, and ten invariants plus `NoDeadlock` passing. The view draws 12
states and 17 arrows; TLC checks transitions omitted from the diagram.

This model checks only its abstract transition system. It does not establish
live DO health, host identity, source artifacts, or release authorization.
