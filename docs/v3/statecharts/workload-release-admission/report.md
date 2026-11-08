# Release

TLC reached 348 states and 1228 steps. The chart draws 18 states and 49 arrows.
The chart does not draw 864 steps that stay in one chart state.

## Rules that hold
TypeOK, OldStopRequiresTargetProof, OldStopRequiresAuthorization, RouteDoesNotMoveBeforeBinding, OldWorkloadRemainsUntilSafeStop, AttemptRequiresPinnedInputs, BindingMatchesPinnedSecret, NoAttemptAfterSecretSnapshotLoss, MissingKeyHoldsUnboundRelease, MissingKeyKeepsOldService, AcceptedPlanHasFences, ReplayReturnsSameAcceptance, NoConcurrentAppWriter, WriterAttemptIsRecorded, RouteReservationHeldUntilTerminal, NoDeadlock
