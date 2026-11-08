# Mini direct V3 upgrade model

Bounded model of the controller-mediated, independent Mini forward transition
and same-target recovery. It does not authorize or perform a Mini operation.

## Reproduce

Run from this directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/MiniDirectUpgrade.tla" \
  --config "$PWD/MiniDirectUpgrade.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

TLC explored 82 states and 164 transitions. All configured invariants and the
deadlock check passed. The compact chart draws 13 states and 15 arrows; omitted
crash and recovery transitions remain in the TLC check.

The model follows protected backup and exact-target signed shadow preparation,
then proves the writer set before journal binding or host effects. It abstracts
cryptographic verification, real writer discovery, Launchd PID reuse, SQL and
filesystem durability, timing, and key custody. See the Mini launch record for
actual transition evidence.
