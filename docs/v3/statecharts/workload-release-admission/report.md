# Release

TLC reached 458 states and 1663 steps. The chart draws 20 states and 43 arrows.
The chart does not draw 1125 steps that stay in one chart state.

## Rules that hold
TypeOK, OldStopRequiresTargetProof, OldStopRequiresAuthorization, RouteDoesNotMoveBeforeBinding, OldWorkloadRemainsUntilSafeStop, AttemptRequiresPinnedInputs, AttemptRequiresStageReservation, BindingMatchesPinnedSecret, NoAttemptAfterSecretSnapshotLoss, MissingKeyHoldsUnboundRelease, MissingKeyKeepsOldService, AcceptedPlanHasFences, IncompleteAdmissionIsRejected, AcceptedPlanHasCompleteEvidence, ReplayReturnsSameAcceptance, NoConcurrentAppWriter, WriterAttemptIsRecorded, RouteReservationHeldUntilTerminal, NoDeadlock
