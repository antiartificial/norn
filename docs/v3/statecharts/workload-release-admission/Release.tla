---------------- MODULE Release ----------------
EXTENDS Naturals, TLC

CONSTANTS AdmissionEvidence, RequiredAdmissionEvidence

VARIABLES phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
          route, oldRunning, proofFresh, stopped, stopHadProof,
          planSecretRevision, currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable,
          inputsBound, boundSecretRevision, requestKeySeen, acceptedPlanDigest,
          replayReturnedOriginal, appFenceHeld, routeReserved, writerAttempted,
          competingWriter
vars == <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
          route, oldRunning, proofFresh, stopped, stopHadProof,
          planSecretRevision, currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable,
          inputsBound, boundSecretRevision, requestKeySeen, acceptedPlanDigest,
          replayReturnedOriginal, appFenceHeld, routeReserved, writerAttempted,
          competingWriter>>

Init ==
  /\ phase = "request"
  /\ claim = 1
  /\ stageReserved = FALSE
  /\ attempt = FALSE
  /\ requestInFlight = FALSE
  /\ job = "absent"
  /\ binding = FALSE
  /\ targetHealthy = FALSE
  /\ route = "old-only"
  /\ oldRunning = TRUE
  /\ proofFresh = FALSE
  /\ stopped = FALSE
  /\ stopHadProof = FALSE
  /\ planSecretRevision = "secret-revision-a"
  /\ currentSecretRevision = "secret-revision-a"
  /\ secretSnapshotAvailable = TRUE
  /\ inputKeyAvailable = TRUE
  /\ inputsBound = FALSE
  /\ boundSecretRevision = "none"
  /\ requestKeySeen = FALSE
  /\ acceptedPlanDigest = "none"
  /\ replayReturnedOriginal = FALSE
  /\ appFenceHeld = FALSE
  /\ routeReserved = FALSE
  /\ writerAttempted = FALSE
  /\ competingWriter = FALSE

AcceptSignedPlan ==
  /\ phase = "request"
  /\ AdmissionEvidence = RequiredAdmissionEvidence
  /\ ~requestKeySeen
  /\ requestKeySeen' = TRUE
  /\ acceptedPlanDigest' = "sha256:accepted-plan"
  /\ appFenceHeld' = TRUE
  /\ routeReserved' = TRUE
  /\ phase' = "accepted"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable, inputsBound,
      boundSecretRevision, replayReturnedOriginal, writerAttempted, competingWriter>>

RejectIncompleteAdmission ==
  /\ phase = "request"
  /\ AdmissionEvidence # RequiredAdmissionEvidence
  /\ phase' = "rejected"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable, inputsBound,
      boundSecretRevision, requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
      appFenceHeld, routeReserved, writerAttempted, competingWriter>>

ReplaySameRequest ==
  /\ requestKeySeen
  /\ acceptedPlanDigest = "sha256:accepted-plan"
  /\ replayReturnedOriginal' = TRUE
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable, inputsBound,
      boundSecretRevision, requestKeySeen, acceptedPlanDigest, appFenceHeld,
      routeReserved, writerAttempted, competingWriter>>

CompetingWriterBlocked ==
  /\ appFenceHeld
  /\ ~writerAttempted
  /\ writerAttempted' = TRUE
  /\ competingWriter' = FALSE
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable, inputsBound,
      boundSecretRevision, requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
      appFenceHeld, routeReserved>>

ResolvePinnedInputs ==
  /\ phase = "accepted"
  /\ requestKeySeen
  /\ secretSnapshotAvailable
  /\ inputKeyAvailable
  /\ inputsBound = FALSE
  /\ inputsBound' = TRUE
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, boundSecretRevision>>

RotateCurrentSecret ==
  /\ currentSecretRevision = "secret-revision-a"
  /\ currentSecretRevision' = "secret-revision-b"
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      secretSnapshotAvailable, inputsBound, boundSecretRevision>>

LosePinnedSecretSnapshot ==
  /\ phase = "accepted"
  /\ ~binding
  /\ secretSnapshotAvailable
  /\ secretSnapshotAvailable' = FALSE
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, inputsBound, boundSecretRevision>>

LosePinnedInputKey ==
  /\ phase \in {"accepted", "attempt-marked", "submitting", "submitted", "ambiguous"}
  /\ ~binding
  /\ inputKeyAvailable
  /\ inputKeyAvailable' = FALSE
  /\ phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputsBound, boundSecretRevision>>

HoldOnMissingPinnedInputs ==
  /\ phase \notin {"complete", "held"}
  /\ (~secretSnapshotAvailable \/ ~inputKeyAvailable)
  /\ phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputsBound, boundSecretRevision>>

MarkAttempt ==
  /\ phase = "accepted"
  /\ stageReserved
  /\ requestKeySeen
  /\ inputsBound
  /\ secretSnapshotAvailable
  /\ inputKeyAvailable
  /\ ~attempt
  /\ attempt' = TRUE
  /\ phase' = "attempt-marked"
  /\ UNCHANGED <<claim, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

ReserveStageEffect ==
  /\ phase = "accepted"
  /\ inputsBound
  /\ ~stageReserved
  /\ stageReserved' = TRUE
  /\ UNCHANGED <<phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputKeyAvailable, inputsBound,
      boundSecretRevision, requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
      appFenceHeld, routeReserved, writerAttempted, competingWriter>>

SendCreate ==
  /\ phase = "attempt-marked"
  /\ attempt
  /\ inputsBound
  /\ secretSnapshotAvailable
  /\ inputKeyAvailable
  /\ job = "absent"
  /\ requestInFlight' = TRUE
  /\ phase' = "submitting"
  /\ UNCHANGED <<claim, stageReserved, attempt, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

NomadAccepts ==
  /\ phase = "submitting"
  /\ requestInFlight
  /\ job = "absent"
  /\ job' = "matching"
  /\ requestInFlight' = FALSE
  /\ phase' = "submitted"
  /\ UNCHANGED <<claim, stageReserved, attempt, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

LoseResponse ==
  /\ phase = "submitting"
  /\ requestInFlight
  /\ job = "absent"
  /\ job' = "matching"
  /\ requestInFlight' = FALSE
  /\ phase' = "ambiguous"
  /\ UNCHANGED <<claim, stageReserved, attempt, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

CrashAndReclaim ==
  /\ (phase \in {"attempt-marked", "submitting", "submitted", "ambiguous", "bound", "warming", "old-only", "mixed", "target-only", "draining"}
      \/ (phase = "accepted" /\ stageReserved))
  /\ claim < 2
  /\ claim' = claim + 1
  /\ UNCHANGED <<phase, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

ObserveMissingAfterAttempt ==
  /\ phase \in {"attempt-marked", "submitting", "ambiguous"}
  /\ claim > 1
  /\ job = "absent"
  /\ phase' = "held"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

ObserveMatchingJob ==
  /\ phase \in {"submitted", "ambiguous", "attempt-marked", "submitting"}
  /\ claim > 1
  /\ attempt
  /\ inputsBound
  /\ inputKeyAvailable
  /\ job = "matching"
  /\ binding' = TRUE
  /\ boundSecretRevision' = planSecretRevision
  /\ phase' = "bound"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision, currentSecretRevision, secretSnapshotAvailable, inputsBound>>

ObserveMismatch ==
  /\ phase \in {"submitted", "ambiguous", "attempt-marked", "submitting"}
  /\ claim > 1
  /\ job = "foreign"
  /\ phase' = "held"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

TargetBecomesHealthy ==
  /\ phase = "bound"
  /\ binding
  /\ job = "matching"
  /\ targetHealthy' = TRUE
  /\ phase' = "warming"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, route, oldRunning, proofFresh, stopped, stopHadProof>>

ProveOldOnly ==
  /\ phase = "warming"
  /\ targetHealthy
  /\ route = "old-only"
  /\ proofFresh' = TRUE
  /\ phase' = "old-only"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

PublishMixed ==
  /\ phase = "old-only"
  /\ proofFresh
  /\ route' = "mixed"
  /\ proofFresh' = FALSE
  /\ phase' = "mixed"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, oldRunning, stopped, stopHadProof>>

PublishTargetOnly ==
  /\ phase = "mixed"
  /\ targetHealthy
  /\ job = "matching"
  /\ route' = "target-only"
  /\ phase' = "target-only"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, oldRunning, proofFresh, stopped, stopHadProof>>

RefreshTargetOnlyProof ==
  /\ phase = "target-only"
  /\ route = "target-only"
  /\ targetHealthy
  /\ binding
  /\ proofFresh' = TRUE
  /\ phase' = "draining"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

TargetLosesHealth ==
  /\ phase \in {"bound", "warming", "old-only", "mixed", "target-only", "draining"}
  /\ targetHealthy
  /\ targetHealthy' = FALSE
  /\ IF stopped THEN phase' = "held" ELSE phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, route, oldRunning, stopped, stopHadProof>>

StopOld ==
  /\ phase = "draining"
  /\ route = "target-only"
  /\ targetHealthy
  /\ binding
  /\ proofFresh
  /\ oldRunning
  /\ oldRunning' = FALSE
  /\ stopped' = TRUE
  /\ stopHadProof' = TRUE
  /\ phase' = "complete"
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, proofFresh>>

Hold ==
  /\ phase \notin {"request", "complete", "held", "rejected"}
  /\ phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, stageReserved, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

LateAcceptWhileHeld ==
  /\ phase = "held"
  /\ requestInFlight
  /\ job = "absent"
  /\ job' = "matching"
  /\ requestInFlight' = FALSE
  /\ UNCHANGED <<phase, claim, stageReserved, attempt, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

Done == phase \in {"complete", "held", "rejected"} /\ UNCHANGED vars

Next == ((AcceptSignedPlan \/ RejectIncompleteAdmission \/ ReplaySameRequest \/ CompetingWriterBlocked)
          /\ UNCHANGED <<inputKeyAvailable, stageReserved>>) \/
        (((ResolvePinnedInputs \/ RotateCurrentSecret \/ LosePinnedSecretSnapshot \/ HoldOnMissingPinnedInputs \/ ObserveMatchingJob)
          /\ UNCHANGED <<requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
                         appFenceHeld, routeReserved, writerAttempted, competingWriter>>)
          /\ UNCHANGED <<inputKeyAvailable, stageReserved>>) \/
        ((ReserveStageEffect
          /\ UNCHANGED <<planSecretRevision, currentSecretRevision, secretSnapshotAvailable,
                         inputsBound, boundSecretRevision>>)
          /\ UNCHANGED <<requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
                         appFenceHeld, routeReserved, writerAttempted, competingWriter>>
          /\ UNCHANGED <<inputKeyAvailable>>) \/
        (((MarkAttempt \/ SendCreate \/ NomadAccepts \/ LoseResponse \/ CrashAndReclaim \/
          ObserveMissingAfterAttempt \/ ObserveMismatch \/ TargetBecomesHealthy \/
          ProveOldOnly \/ PublishMixed \/ PublishTargetOnly \/ RefreshTargetOnlyProof \/
          TargetLosesHealth \/ StopOld \/ Hold \/ LateAcceptWhileHeld \/ Done)
          /\ UNCHANGED <<planSecretRevision, currentSecretRevision, secretSnapshotAvailable,
                         inputsBound, boundSecretRevision>>)
          /\ UNCHANGED <<requestKeySeen, acceptedPlanDigest, replayReturnedOriginal,
                         appFenceHeld, routeReserved, writerAttempted, competingWriter>>
          /\ UNCHANGED <<inputKeyAvailable, stageReserved>>) \/
        (LosePinnedInputKey /\ UNCHANGED <<requestKeySeen, acceptedPlanDigest,
          replayReturnedOriginal, appFenceHeld, routeReserved, writerAttempted, competingWriter,
          stageReserved>>)

TypeOK ==
  /\ phase \in {"request", "rejected", "accepted", "attempt-marked", "submitting", "submitted", "ambiguous", "bound", "warming", "old-only", "mixed", "target-only", "draining", "complete", "held"}
  /\ claim \in Nat
  /\ stageReserved \in BOOLEAN
  /\ attempt \in BOOLEAN
  /\ requestInFlight \in BOOLEAN
  /\ job \in {"absent", "matching", "foreign"}
  /\ binding \in BOOLEAN
  /\ targetHealthy \in BOOLEAN
  /\ route \in {"old-only", "mixed", "target-only"}
  /\ oldRunning \in BOOLEAN
  /\ proofFresh \in BOOLEAN
  /\ stopped \in BOOLEAN
  /\ stopHadProof \in BOOLEAN
  /\ planSecretRevision \in {"secret-revision-a"}
  /\ currentSecretRevision \in {"secret-revision-a", "secret-revision-b"}
  /\ secretSnapshotAvailable \in BOOLEAN
  /\ inputKeyAvailable \in BOOLEAN
  /\ inputsBound \in BOOLEAN
  /\ boundSecretRevision \in {"none", "secret-revision-a"}
  /\ requestKeySeen \in BOOLEAN
  /\ acceptedPlanDigest \in {"none", "sha256:accepted-plan"}
  /\ replayReturnedOriginal \in BOOLEAN
  /\ appFenceHeld \in BOOLEAN
  /\ routeReserved \in BOOLEAN
  /\ writerAttempted \in BOOLEAN
  /\ competingWriter \in BOOLEAN

OldStopRequiresTargetProof == stopped => stopHadProof
OldStopRequiresAuthorization == stopped => (attempt /\ job = "matching" /\ binding)
RouteDoesNotMoveBeforeBinding == (route # "old-only") => binding
OldWorkloadRemainsUntilSafeStop == ~stopped => oldRunning
BindingRequiresMatchingTarget == binding => job = "matching"
AttemptRequiresPinnedInputs == attempt => (inputsBound /\ requestKeySeen /\ appFenceHeld /\ routeReserved)
AttemptRequiresStageReservation == attempt => stageReserved
BindingMatchesPinnedSecret == binding => (inputsBound /\ boundSecretRevision = planSecretRevision)
NoAttemptAfterSecretSnapshotLoss == ~secretSnapshotAvailable => ~attempt
MissingKeyHoldsUnboundRelease == (~inputKeyAvailable /\ ~binding) => phase = "held"
MissingKeyKeepsOldService == (~inputKeyAvailable /\ ~binding) => (route = "old-only" /\ oldRunning)
AcceptedPlanHasFences == requestKeySeen => (appFenceHeld /\ routeReserved)
IncompleteAdmissionIsRejected == AdmissionEvidence # RequiredAdmissionEvidence =>
  (phase \in {"request", "rejected"} /\ (phase = "rejected" =>
    (~requestKeySeen /\ ~appFenceHeld /\ ~routeReserved)))
AcceptedPlanHasCompleteEvidence == requestKeySeen => AdmissionEvidence = RequiredAdmissionEvidence
ReplayReturnsSameAcceptance == replayReturnedOriginal => (requestKeySeen /\ acceptedPlanDigest = "sha256:accepted-plan")
NoConcurrentAppWriter == appFenceHeld => ~competingWriter
WriterAttemptIsRecorded == writerAttempted => appFenceHeld
RouteReservationHeldUntilTerminal == requestKeySeen /\ phase \notin {"complete", "held"} => routeReserved

Spec == Init /\ [][Next]_vars


============================================================
