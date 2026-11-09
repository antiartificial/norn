# Proposed online Norn platform upgrade

This bounded statechart models a mixed-version platform upgrade after additive
schema expansion. It treats the release as online only when the old service
continues serving through passive candidate verification and a single atomic
traffic/writer handoff. The old writer is fenced before target writes; the old
process is retired only after target serving proof, and schema contraction
waits until then.

A controller crash or ambiguous effect preserves the platform state and holds
the durable intent. Recovery resumes only that intent. Changing the target
while a generation is bound remains held until the operator retries the
journaled target. Candidate failure before handoff leaves the old release
serving.

TLC explored 97 states and 251 steps. All 12 configured invariants and the
deadlock check passed. The chart draws 13 states and 23 arrows. Its view hides
24 repetitive crash/recovery transitions outside representative recovery
boundaries; TLC still checks them. It also omits 83 transitions that stay
within the same displayed state.

## Reproduce

Run from this directory:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/OnlineUpgrade.tla" \
  --config "$PWD/OnlineUpgrade.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

## Limits

This is a proposed protocol model, not a claim that Norn implements online
platform upgrades. It assumes the old binary can read the expanded schema and
the target can start and pass readiness on it. It also assumes the target
remains healthy after handoff. The model abstracts migration execution,
registered writer discovery, lease fencing, process supervision, serving
topology, post-handoff target failures, data integrity, duration, and durable
receipt implementation. The first Mini upgrade used a bounded service fence;
it is not evidence for this online sequence. Before implementation, map each
modeled action to an idempotent controller phase and each invariant to a
compatibility contract and recovery test.
