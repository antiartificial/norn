# WorkloadStopRecovery

TLC reached 10 states and 15 steps. The chart draws 8 states and 8 arrows.
The chart does not draw 6 steps that stay in one chart state.
The view hides 1 steps, so the chart does not draw them.

## Rules that hold
StopWrittenAtMostOnce, MarkerRequiresOneWrite, ReceiptRequiresIntentMarker, RecoveryDoesNotRewriteStop, CompletionRequiresTerminalAllocations, StopRequiresHealthyMixedRoute, NoDeadlock
