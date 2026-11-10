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

The model includes the observed halt after host-agent fencing: an ordinary
same-intent retry cannot cross that gate; a distinct signed repair controller
must verify source lineage and resume the exact generation. TLC output and
counts are recorded in `report.md` after reproduction.

The model starts at the Mini's observed schema 51 and follows protected backup
and exact-target signed shadow preparation, then proves the writer set before
journal binding or host effects. Its forward migration advances to schema 52.
The repair
controller action abstracts its signature, distinct source identity,
source-lineage verification, Launchd PID reuse, SQL and filesystem durability,
timing, and key custody. It also abstracts the adopted-source to journal-source
relationship and the GUI LaunchAgent environment used by the API launcher. The
recovered-baseline integration tests cover the signed-ancestor finalization
gate; the model does not prove that implementation detail. See the Mini launch
record for actual transition evidence.
