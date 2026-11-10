# MiniDirectUpgrade

TLC reached 90 states and 179 steps. The chart draws 14 states and 18 arrows.
The chart does not draw 7 steps that stay in one chart state.
The view hides 83 steps, so the chart does not draw them.

## Rules that hold
TypeOK, MigrationRequiresFencedWriters, NoOldWriterAfterSchemaChange, TargetRequiresBoundVerifiedCandidate, ReceiptRequiresSuccessfulPostflight, ChangedIntentStaysHeld, RepairGatePreservesObservedMiniState, NoDeadlock
