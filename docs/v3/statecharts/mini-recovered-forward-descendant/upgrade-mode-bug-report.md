# RecoveryDescendant

TLC reached 6 states and 7 steps. The chart draws 5 states and 5 arrows.
The view hides 2 steps, so the chart does not draw them.

## VerifiedLaterReleaseRemainsUpgradeable breaks
The shortest path has 3 steps:
0. Recovery receipt verified; recovery release active
1. check retained evidence and current signed lineage: First forward upgrade allowed
2. promote the first signed descendant through the controller: Signed descendant active; recovery receipt retained
3. request upgrade while the operation mode is not admitted: Upgrade held by operation-mode or recovery-lineage gate

## Rules that hold
TypeOK, GateBindsRuntimeToSignedDescendant, UnrelatedReleaseCannotPassGate, RecoveryEvidenceIsNeverDiscarded, NoDeadlock
