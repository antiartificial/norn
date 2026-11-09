# Existing Fleet Genesis model

Bounded model of dormant controller installation and first Genesis activation
on the existing three-control DO staging Fleet. A fresh-Fleet request is
explicitly blocked: provider creation belongs to the separate plan/apply lane.

## Reproduce

Run from this directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/Genesis.tla" \
  --config "$PWD/Genesis.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

TLC checked 69 distinct states and 107 transitions; all nine invariants and
`NoDeadlock` passed. The diagram draws 16 states and 18 arrows. It abstracts the
individual signed inputs as one valid/invalid gate and does not prove provider
API behavior, credentials, host identity, SSH, or real service health.
