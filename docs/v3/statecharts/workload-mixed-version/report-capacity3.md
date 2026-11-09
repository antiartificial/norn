# WorkloadMixedVersion

TLC reached 24 states and 52 steps. The chart draws 9 states and 11 arrows.
The chart does not draw 1 steps that stay in one chart state.
The view hides 34 steps, so the chart does not draw them.

## Rules that hold
CapacityBound, AllocationBounds, ReadyBound, RouteHasReadyTarget, NoOldRetirementBeforeRouteProof, NoOldRetirementBeforeTargetOnlyRoute, NoTargetRouteWithoutFullReadiness, MixedVersionsRequireCompatibility, NoSchemaContractionWithOldWriters, TerminalStateSafe, HoldIsExplicit, NoDeadlock
