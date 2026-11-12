# Held workload release abandonment and key retention

This bounded offline model proposes a fail-closed disposition for a workload
release that has reached a terminal held receipt. It covers one held release
input and one unrelated retained input that uses the same key-ring key version.
It does not implement or qualify an HTTP endpoint, external-effect observer,
operator identity, cryptographic key service, or production worker recovery.

The proposed order is:

1. Keep the accepted private input when a release is held or its worker is
   lost.
2. Let a successor claim read-only reconciliation. Do not resume the terminal
   held intent.
3. Require readback that settles the external effects before accepting an
   explicit owner authorization to abandon.
4. Persist a durable abandonment receipt before deleting that release's input
   and writing its tombstone.
5. Retire a key-ring key version only after a fresh inventory proves that no
   retained input record references it. A tombstone for one operation does not
   prove that the shared key is unused.

The baseline checks 38 states / 77 steps, four safety invariants, and deadlock.
Three mutants reproduce violations: abandoning before effect readback or owner
authorization, deleting an input while its release remains held, and retiring
a key while another input still depends on it. Their shortest paths are in
[`mutants/`](mutants/); the baseline statechart is in [`baseline/`](baseline/).

## Current implementation boundary

`DeleteCompletedWorkloadReleasePrivateInput` currently permits cleanup only
after a validated `Complete` receipt. Pending and held inputs remain stored.
`WorkloadReleaseInputKeyRing` wraps a per-record data key under a key-ring key
version, and startup's `RequiredWorkloadReleaseInputKeys` discovers versions
still referenced by stored records. There is no workload-release abandonment
receipt or held-input cleanup path. This model therefore describes the policy
needed before adding that path; it is not evidence that abandonment is
implemented.

The model abstracts external-effect reconciliation as a single boolean. A real
implementation must bind the readback to the exact operation, app, source and
target generation, current route, allocations, and durable receipt. It must
also distinguish deleting one encrypted record from retiring a shared key
version, and make both operations replay-safe across process restart.

## Reproduce

Run from this directory with the `statechart` skill:

```sh
bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/HeldReleaseAbandonment.tla" \
  --config "$PWD/HeldReleaseAbandonment.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-baseline"

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/HeldReleaseAbandonment.tla" \
  --config "$PWD/UnsafeAbandon.cfg" --view "$PWD/view.ts" \
  --out "$PWD/reproduced-unsafe-abandon"

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/HeldReleaseAbandonment.tla" \
  --config "$PWD/UnsafeDelete.cfg" --view "$PWD/view.ts" \
  --out "$PWD/reproduced-unsafe-delete"

bun /Users/arti/.agents/skills/statechart/scripts/statechart.ts \
  "$PWD/HeldReleaseAbandonment.tla" \
  --config "$PWD/UnsafeKeyRetire.cfg" --view "$PWD/view.ts" \
  --out "$PWD/reproduced-unsafe-key-retire"
```

The diagrams omit repeated self-loops and some internal transitions; TLC
checks all model transitions. No model path has been replayed against a
production abandonment route because no such route exists yet.
