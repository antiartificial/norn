# AdmissionReplay

TLC reached 115 states and 365 steps. The chart draws 12 states and 30 arrows.
The chart does not draw 190 steps that stay in one chart state.

## Rules that hold
TypeOK, AcceptanceHasVerifiedSource, SameKeyReplayReturnsStoredReceipt, ChangedQualificationConflicts, ReplayDoesNotRereadSource, OriginalReplayUsesAcceptedRecord, AcceptanceWriteIsUnique, LostResponseLeavesDurableReceipt, ReplayUsesDurableReceipt, ReplayHydratesRestartedController, NoDeadlock
