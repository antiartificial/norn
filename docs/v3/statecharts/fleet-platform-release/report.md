# NornRelease

TLC reached 299 states and 1036 steps. The chart draws 12 states and 17 arrows.
The view hides 587 steps, so the chart does not draw them.

## Rules that hold
NoMutationWithoutIntent, StopsRequireAllStaged, PromotesRequireAllStopped, StartsRequireTargetPromoted, TargetStartHasTargetBytes, PinsRequireHealthyTarget, RunnerIsOneUse, RunnerHasDurableIntent, UnknownStateHolds, NoDeadlock
