# OnlineUpgrade

TLC reached 97 states and 251 steps. The chart draws 13 states and 23 arrows.
The chart does not draw 83 steps that stay in one chart state.
The view hides 24 steps, so the chart does not draw them.

## Rules that hold
TypeOK, OnlineAvailability, SingleServingWriter, NoContractWhileOldWriterRuns, OldBinaryCanReadSchema, TargetMustBeCompatibleBeforeServing, TargetWritesRequireOldWriterFenced, TargetWriterRequiresOldFence, NoRollbackAfterTargetWrite, ChangedIntentRemainsHeld, ReceiptRequiresVerifiedTerminalState, ContractedSchemaRequiresHealthyTarget, NoDeadlock
