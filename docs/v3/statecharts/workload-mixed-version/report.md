# WorkloadMixedVersion

TLC reached 103 states and 275 steps. The chart draws 14 states and 19 arrows.
The chart does not draw 3 steps that stay in one chart state.
The view hides 186 steps, so the chart does not draw them.

## Rules that hold
CapacityBound, AllocationBounds, ReadyBound, RouteHasReadyTarget, NoOldRetirementBeforeRouteProof, NoOldRetirementBeforeTargetOnlyRoute, NoTargetRouteWithoutFullReadiness, MixedVersionsRequireCompatibility, NoSchemaContractionWithOldWriters, TerminalStateSafe, HoldIsExplicit, NoDeadlock
