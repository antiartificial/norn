# Mini queued-upgrade host-agent stability gate

Finite model of the recovered-state precondition that checks whether the
managed host agent matches the active signed release and remains stable across
three consecutive one-second observations. The implementation permits up to
30 observations. The model uses a smaller five-sample bound so it can show
both admission after a restart settles and a held result when the process keeps
changing.

## Reproduce

```sh
bun /Users/arti/.codex/skills/statechart/scripts/statechart.ts \
  "$PWD/HostAgentStability.tla" \
  --config "$PWD/HostAgentStability.cfg" \
  --view "$PWD/view.ts" \
  --out "$PWD/reproduced-output"
```

The model checks that admission requires three stable launchd/agent process
samples, a missing agent resets the stability window, and exhausting the sample
budget leaves the active source and schema untouched. The current implementation
checks the LaunchAgent wrapper's direct child. The latest queued operation
reported no child for its bounded stability window, while a later idle readback
found the managed agent running. The model represents the missing-agent case
but abstracts the process-tree lookup, candidate byte comparison, signature
and Git ancestry checks, actual launchd behavior, host API evidence, and all
work after admission. The chart hides some launchd
restart and repeated-sample transitions that do not change the reader-visible
sample count. TLC checks safety invariants and deadlock; the model does not
prove eventual admission or controller/runtime refinement.
