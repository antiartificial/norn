# HeldReleaseAbandonment

TLC reached 74 states and 212 steps. The chart draws 13 states and 25 arrows.
The chart does not draw 127 steps that stay in one chart state.
The view hides 3 steps, so the chart does not draw them.

## InputDeletionRequiresTerminalDisposition breaks
The shortest path has 2 steps:
0. Release active; input retained
1. release enters a durable hold: Held; effects unresolved; input retained
2. unsafe path deletes inputs while the operation remains held: Held; input deleted before terminal disposition

## Rules that hold
AbandonRequiresReadbackAndAuthority, OtherInputDeletionIsTombstoned, NoKeyRetirementWhileInputsRemain, NoDeadlock
