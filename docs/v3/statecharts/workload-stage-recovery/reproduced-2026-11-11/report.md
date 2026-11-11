# WorkloadStageRecovery

TLC reached 26 states and 31 steps. The chart draws 15 states and 18 arrows.
The chart does not draw 3 steps that stay in one chart state.
The view hides 2 steps, so the chart does not draw them.

## Rules that hold
NoDuplicateCreate, CreateRequiresDurableMarker, BoundTargetWasReadBack, RouteRequiresHealthyBoundTarget, OldStopsOnlyAfterTargetRoute, TerminalRequiresTargetRoute, NoTerminalWhileCreateUnresolved, NoDeadlock
