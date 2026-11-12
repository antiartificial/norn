# HeldReleaseAbandonment

TLC reached 70 states and 174 steps. The chart draws 13 states and 25 arrows.
The chart does not draw 98 steps that stay in one chart state.
The view hides 6 steps, so the chart does not draw them.

## NoKeyRetirementWhileInputsRemain breaks
The shortest path has 1 step:
0. Release active; input retained
1. unsafe path retires a key still needed by an input: Key retired while retained input still exists

## Rules that hold
AbandonRequiresReadbackAndAuthority, InputDeletionRequiresTerminalDisposition, OtherInputDeletionIsTombstoned, NoDeadlock
