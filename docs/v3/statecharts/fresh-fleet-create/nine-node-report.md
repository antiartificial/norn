# FreshFleetNineNodeRecovery

TLC reached 39 states and 58 steps. The chart draws 19 states and 23 arrows.
The chart does not draw 32 steps that stay in one chart state.
The view hides 1 steps, so the chart does not draw them.

## Rules that hold
TypeOK, EffectsRequireBoundIntent, StateUsesExactProviderIDs, ReadBackMustMatchObserved, IntentBindsReviewedPlanAndState, RecoveryUsesSameIntent, BootstrapRequiresAllNineExact, NoConfigureWithoutBootstrapEvidence, TerminalReceiptRequiresRunnerRetirement, NoEffectsWhileHeld, NoDeadlock
