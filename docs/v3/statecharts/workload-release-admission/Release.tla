---------------- MODULE Release ----------------
EXTENDS Naturals, TLC

VARIABLES phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
          route, oldRunning, proofFresh, stopped, stopHadProof,
          planSecretRevision, currentSecretRevision, secretSnapshotAvailable,
          inputsBound, boundSecretRevision
vars == <<phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
          route, oldRunning, proofFresh, stopped, stopHadProof,
          planSecretRevision, currentSecretRevision, secretSnapshotAvailable,
          inputsBound, boundSecretRevision>>

Init ==
  /\ phase = "accepted"
  /\ claim = 1
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
  /\ inputsBound = FALSE
  /\ boundSecretRevision = "none"

ResolvePinnedInputs ==
  /\ phase = "accepted"
  /\ secretSnapshotAvailable
  /\ inputsBound = FALSE
  /\ inputsBound' = TRUE
  /\ UNCHANGED <<phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, boundSecretRevision>>

RotateCurrentSecret ==
  /\ currentSecretRevision = "secret-revision-a"
  /\ currentSecretRevision' = "secret-revision-b"
  /\ UNCHANGED <<phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      secretSnapshotAvailable, inputsBound, boundSecretRevision>>

LosePinnedSecretSnapshot ==
  /\ phase = "accepted"
  /\ ~binding
  /\ secretSnapshotAvailable
  /\ secretSnapshotAvailable' = FALSE
  /\ UNCHANGED <<phase, claim, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, inputsBound, boundSecretRevision>>

HoldOnMissingPinnedInputs ==
  /\ phase \notin {"complete", "held"}
  /\ ~secretSnapshotAvailable
  /\ phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy,
      route, oldRunning, stopped, stopHadProof, planSecretRevision,
      currentSecretRevision, secretSnapshotAvailable, inputsBound, boundSecretRevision>>

MarkAttempt ==
  /\ phase = "accepted"
  /\ inputsBound
  /\ secretSnapshotAvailable
  /\ ~attempt
  /\ attempt' = TRUE
  /\ phase' = "attempt-marked"
  /\ UNCHANGED <<claim, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

SendCreate ==
  /\ phase = "attempt-marked"
  /\ attempt
  /\ inputsBound
  /\ secretSnapshotAvailable
  /\ job = "absent"
  /\ requestInFlight' = TRUE
  /\ phase' = "submitting"
  /\ UNCHANGED <<claim, attempt, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

NomadAccepts ==
  /\ requestInFlight
  /\ job = "absent"
  /\ job' = "matching"
  /\ requestInFlight' = FALSE
  /\ phase' = "submitted"
  /\ UNCHANGED <<claim, attempt, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

LoseResponse ==
  /\ requestInFlight
  /\ job = "matching"
  /\ requestInFlight' = FALSE
  /\ phase' = "ambiguous"
  /\ UNCHANGED <<claim, attempt, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

CrashAndReclaim ==
  /\ phase \in {"attempt-marked", "submitting", "submitted", "ambiguous", "bound", "warming", "old-only", "mixed", "target-only", "draining"}
  /\ claim < 2
  /\ claim' = claim + 1
  /\ UNCHANGED <<phase, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

ObserveMissingAfterAttempt ==
  /\ phase \in {"attempt-marked", "submitting", "ambiguous"}
  /\ claim > 1
  /\ job = "absent"
  /\ phase' = "held"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

ObserveMatchingJob ==
  /\ phase \in {"submitted", "ambiguous", "attempt-marked", "submitting"}
  /\ claim > 1
  /\ attempt
  /\ inputsBound
  /\ job = "matching"
  /\ binding' = TRUE
  /\ boundSecretRevision' = planSecretRevision
  /\ phase' = "bound"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof, planSecretRevision, currentSecretRevision, secretSnapshotAvailable, inputsBound>>

ObserveMismatch ==
  /\ phase \in {"submitted", "ambiguous", "attempt-marked", "submitting"}
  /\ claim > 1
  /\ job = "foreign"
  /\ phase' = "held"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

TargetBecomesHealthy ==
  /\ phase = "bound"
  /\ binding
  /\ job = "matching"
  /\ targetHealthy' = TRUE
  /\ phase' = "warming"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, route, oldRunning, proofFresh, stopped, stopHadProof>>

ProveOldOnly ==
  /\ phase = "warming"
  /\ targetHealthy
  /\ route = "old-only"
  /\ proofFresh' = TRUE
  /\ phase' = "old-only"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

PublishMixed ==
  /\ phase = "old-only"
  /\ proofFresh
  /\ route' = "mixed"
  /\ proofFresh' = FALSE
  /\ phase' = "mixed"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, oldRunning, stopped, stopHadProof>>

PublishTargetOnly ==
  /\ phase = "mixed"
  /\ targetHealthy
  /\ job = "matching"
  /\ route' = "target-only"
  /\ phase' = "target-only"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, oldRunning, proofFresh, stopped, stopHadProof>>

RefreshTargetOnlyProof ==
  /\ phase = "target-only"
  /\ route = "target-only"
  /\ targetHealthy
  /\ binding
  /\ proofFresh' = TRUE
  /\ phase' = "draining"
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

TargetLosesHealth ==
  /\ targetHealthy
  /\ targetHealthy' = FALSE
  /\ IF stopped THEN phase' = "held" ELSE phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, route, oldRunning, stopped, stopHadProof>>

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
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, proofFresh>>

Hold ==
  /\ phase \notin {"complete", "held"}
  /\ phase' = "held"
  /\ proofFresh' = FALSE
  /\ UNCHANGED <<claim, attempt, requestInFlight, job, binding, targetHealthy, route, oldRunning, stopped, stopHadProof>>

LateAcceptWhileHeld ==
  /\ phase = "held"
  /\ requestInFlight
  /\ job = "absent"
  /\ job' = "matching"
  /\ requestInFlight' = FALSE
  /\ UNCHANGED <<phase, claim, attempt, binding, targetHealthy, route, oldRunning, proofFresh, stopped, stopHadProof>>

Done == phase \in {"complete", "held"} /\ UNCHANGED vars

Next == ResolvePinnedInputs \/ RotateCurrentSecret \/ LosePinnedSecretSnapshot \/ HoldOnMissingPinnedInputs \/
        ObserveMatchingJob \/
        ((MarkAttempt \/ SendCreate \/ NomadAccepts \/ LoseResponse \/ CrashAndReclaim \/
          ObserveMissingAfterAttempt \/ ObserveMismatch \/ TargetBecomesHealthy \/
          ProveOldOnly \/ PublishMixed \/ PublishTargetOnly \/ RefreshTargetOnlyProof \/
          TargetLosesHealth \/ StopOld \/ Hold \/ LateAcceptWhileHeld \/ Done)
          /\ UNCHANGED <<planSecretRevision, currentSecretRevision, secretSnapshotAvailable,
                         inputsBound, boundSecretRevision>>)

TypeOK ==
  /\ phase \in {"accepted", "attempt-marked", "submitting", "submitted", "ambiguous", "bound", "warming", "old-only", "mixed", "target-only", "draining", "complete", "held"}
  /\ claim \in Nat
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
  /\ inputsBound \in BOOLEAN
  /\ boundSecretRevision \in {"none", "secret-revision-a"}

OldStopRequiresTargetProof == stopped => stopHadProof
OldStopRequiresAuthorization == stopped => (attempt /\ job = "matching" /\ binding)
RouteDoesNotMoveBeforeBinding == (route # "old-only") => binding
OldWorkloadRemainsUntilSafeStop == ~stopped => oldRunning
BindingRequiresMatchingTarget == binding => job = "matching"
AttemptRequiresPinnedInputs == attempt => inputsBound
BindingMatchesPinnedSecret == binding => (inputsBound /\ boundSecretRevision = planSecretRevision)
NoAttemptAfterSecretSnapshotLoss == ~secretSnapshotAvailable => ~attempt

Spec == Init /\ [][Next]_vars


============================================================
