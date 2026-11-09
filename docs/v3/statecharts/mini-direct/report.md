# MiniDirectUpgrade

TLC reached 82 states and 164 steps. The chart draws 13 states and 15 arrows.
The chart does not draw 7 steps that stay in one chart state.
The view hides 83 steps, so the chart does not draw them.

## Rules that hold
TypeOK, MigrationRequiresFencedWriters, NoOldWriterAfterSchemaChange, TargetRequiresBoundVerifiedCandidate, ReceiptRequiresSuccessfulPostflight, ChangedIntentStaysHeld, NoDeadlock
