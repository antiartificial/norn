# FreshStagingRecovery

TLC reached 102 states and 210 steps. The chart draws 17 states and 35 arrows.
The chart does not draw 55 steps that stay in one chart state.
The view hides 13 steps, so the chart does not draw them.

## Rules that hold
TypeOK, NoCreateBeforeIntent, UnknownEffectIsHeld, NoRecoveryWriteWithoutExactReadback, NoDuplicateProviderCreate, NoConfigurationBeforeFullInventory, NoInventoryFromPartialState, NoTerminalReceiptFromPartialFleet, UnknownProviderStateHolds, NoDeadlock
