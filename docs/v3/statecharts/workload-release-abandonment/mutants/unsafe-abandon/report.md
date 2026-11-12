# HeldReleaseAbandonment

TLC reached 54 states and 105 steps. The chart draws 13 states and 19 arrows.
The chart does not draw 48 steps that stay in one chart state.
The view hides 5 steps, so the chart does not draw them.

## AbandonRequiresReadbackAndAuthority breaks
The shortest path has 2 steps:
0. Release active; input retained
1. release enters a durable hold: Held; effects unresolved; input retained
2. controller records the abandonment receipt: Abandonment recorded without safe readback or authority

## NoDeadlock breaks
The shortest path has 3 steps:
0. Release active; input retained
1. another operation deletes its input with a terminal receipt: Release active; input retained
2. release enters a durable hold: Held; effects unresolved; input retained
3. controller records the abandonment receipt: Abandonment recorded without safe readback or authority

## Rules that hold
InputDeletionRequiresTerminalDisposition, OtherInputDeletionIsTombstoned, NoKeyRetirementWhileInputsRemain
