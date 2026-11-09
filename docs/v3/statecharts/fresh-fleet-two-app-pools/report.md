# FreshTwoAppPools

TLC reached 45 states and 98 steps. The chart draws 18 states and 26 arrows.
The chart does not draw 21 steps that stay in one chart state.
The view hides 24 steps, so the chart does not draw them.

## Rules that hold
TypeOK, NoCreateBeforeIntent, UnknownEffectIsHeld, NoRecoveryWriteWithoutReadback, NoDuplicateProviderCreate, NoInventoryFromPartialPools, NoConfigurationBeforeFullInventory, NoTerminalReceiptFromPartialFleet, NoSuccessWithMismatchedProvider, GeneralWorkloadOnGeneralPool, StatefulWorkloadOnStatefulPool, GeneralReadinessRequiresPlacement, StatefulReadinessRequiresPlacement, StatefulSuccessRequiresRecovery, NoDeadlock
