---------------- MODULE AdmissionReplay ----------------
EXTENDS Naturals, TLC

CONSTANTS QualificationID, AllowedReplicas, OutOfPolicyReplicas

VARIABLES phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
          qualificationValid, sourceAvailable, sourceReads, replayResult, controllerAlive,
          processLocalAcceptance, durableReceipt, acceptanceWrites, initialResponseObserved
vars == <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
          qualificationValid, sourceAvailable, sourceReads, replayResult, controllerAlive,
          processLocalAcceptance, durableReceipt, acceptanceWrites, initialResponseObserved>>

Init ==
  /\ phase = "request"
  /\ selectedQualification = QualificationID
  /\ selectedReplicas = 2
  /\ acceptedQualification = "none"
  /\ acceptedReplicas = 0
  /\ keyBound = FALSE
  /\ qualificationValid = TRUE
  /\ sourceAvailable = TRUE
  /\ sourceReads = 0
  /\ replayResult = "none"
  /\ controllerAlive = TRUE
  /\ processLocalAcceptance = FALSE
  /\ durableReceipt = "none"
  /\ acceptanceWrites = 0
  /\ initialResponseObserved = FALSE

CommitVerifiedRequest(responseVisible) ==
  /\ phase = "request"
  /\ ~keyBound
  /\ qualificationValid
  /\ sourceAvailable
  /\ selectedReplicas \in AllowedReplicas
  /\ controllerAlive
  /\ keyBound' = TRUE
  /\ acceptedQualification' = selectedQualification
  /\ acceptedReplicas' = selectedReplicas
  /\ sourceReads' = sourceReads + 1
  /\ phase' = "accepted"
  /\ durableReceipt' = "receipt-1"
  /\ acceptanceWrites' = acceptanceWrites + 1
  /\ processLocalAcceptance' = TRUE
  /\ initialResponseObserved' = responseVisible
  /\ UNCHANGED <<selectedQualification, selectedReplicas, qualificationValid, sourceAvailable, replayResult,
                  controllerAlive>>

AcceptVerifiedQualification == CommitVerifiedRequest(TRUE)

CommitAcceptanceLoseResponse == CommitVerifiedRequest(FALSE)

RejectOutOfPolicySize ==
  /\ phase = "request"
  /\ selectedReplicas = OutOfPolicyReplicas
  /\ phase' = "rejected"
  /\ UNCHANGED <<selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, replayResult, controllerAlive,
                  processLocalAcceptance, durableReceipt, acceptanceWrites, initialResponseObserved>>

QualificationExpires ==
  /\ keyBound
  /\ qualificationValid
  /\ qualificationValid' = FALSE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  sourceAvailable, sourceReads, replayResult, controllerAlive, processLocalAcceptance,
                  durableReceipt, acceptanceWrites, initialResponseObserved>>

AcceptedSourceDisappears ==
  /\ keyBound
  /\ sourceAvailable
  /\ sourceAvailable' = FALSE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceReads, replayResult, controllerAlive, processLocalAcceptance,
                  durableReceipt, acceptanceWrites, initialResponseObserved>>

ControllerCrashes ==
  /\ controllerAlive
  /\ keyBound
  /\ durableReceipt = "receipt-1"
  /\ controllerAlive' = FALSE
  /\ processLocalAcceptance' = FALSE
  /\ replayResult' = "none"
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, durableReceipt, acceptanceWrites,
                  initialResponseObserved>>

ControllerRestarts ==
  /\ ~controllerAlive
  /\ controllerAlive' = TRUE
  /\ processLocalAcceptance' = FALSE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, replayResult, durableReceipt, acceptanceWrites,
                  initialResponseObserved>>

RetrySameQualification ==
  /\ controllerAlive
  /\ keyBound
  /\ durableReceipt = "receipt-1"
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ replayResult = "none"
  /\ replayResult' = "original receipt"
  /\ processLocalAcceptance' = TRUE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, controllerAlive, durableReceipt,
                  acceptanceWrites, initialResponseObserved>>

SelectDifferentQualification ==
  /\ keyBound
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ replayResult = "none"
  /\ selectedQualification' = "different-qualification"
  /\ UNCHANGED <<phase, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound, qualificationValid,
                  sourceAvailable, sourceReads, replayResult, controllerAlive, processLocalAcceptance, durableReceipt,
                  acceptanceWrites, initialResponseObserved>>

SelectDifferentReplicaCount ==
  /\ keyBound
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ 3 \in AllowedReplicas
  /\ replayResult = "none"
  /\ selectedReplicas' = 3
  /\ UNCHANGED <<phase, selectedQualification, acceptedQualification, acceptedReplicas, keyBound, qualificationValid,
                  sourceAvailable, sourceReads, replayResult, controllerAlive, processLocalAcceptance, durableReceipt,
                  acceptanceWrites, initialResponseObserved>>

SelectOutOfPolicyReplicaCount ==
  /\ phase = "request"
  /\ selectedReplicas \in AllowedReplicas
  /\ selectedReplicas' = OutOfPolicyReplicas
  /\ UNCHANGED <<phase, selectedQualification, acceptedQualification, acceptedReplicas, keyBound, qualificationValid,
                  sourceAvailable, sourceReads, replayResult, controllerAlive, processLocalAcceptance, durableReceipt,
                  acceptanceWrites, initialResponseObserved>>

RetryChangedQualification ==
  /\ controllerAlive
  /\ keyBound
  /\ durableReceipt = "receipt-1"
  /\ (selectedQualification # acceptedQualification \/ selectedReplicas # acceptedReplicas)
  /\ replayResult = "none"
  /\ replayResult' = "conflict"
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, controllerAlive, processLocalAcceptance,
                  durableReceipt, acceptanceWrites, initialResponseObserved>>

Done == (replayResult # "none" \/ phase = "rejected") /\ UNCHANGED vars

Next == AcceptVerifiedQualification \/ CommitAcceptanceLoseResponse \/ RejectOutOfPolicySize
        \/ SelectOutOfPolicyReplicaCount \/ QualificationExpires \/ AcceptedSourceDisappears
        \/ ControllerCrashes \/ ControllerRestarts \/ RetrySameQualification
        \/ SelectDifferentQualification \/ SelectDifferentReplicaCount
        \/ RetryChangedQualification \/ Done

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in {"request", "accepted", "rejected"}
  /\ selectedQualification \in {QualificationID, "different-qualification"}
  /\ selectedReplicas \in AllowedReplicas \cup {OutOfPolicyReplicas}
  /\ acceptedQualification \in {"none", QualificationID}
  /\ acceptedReplicas \in {0} \cup AllowedReplicas
  /\ keyBound \in BOOLEAN
  /\ qualificationValid \in BOOLEAN
  /\ sourceAvailable \in BOOLEAN
  /\ sourceReads \in Nat
  /\ replayResult \in {"none", "original receipt", "conflict"}
  /\ controllerAlive \in BOOLEAN
  /\ processLocalAcceptance \in BOOLEAN
  /\ durableReceipt \in {"none", "receipt-1"}
  /\ acceptanceWrites \in Nat
  /\ initialResponseObserved \in BOOLEAN

AcceptanceHasVerifiedSource ==
  keyBound => acceptedQualification = QualificationID /\ sourceReads = 1 /\ acceptedReplicas \in AllowedReplicas
                 /\ durableReceipt = "receipt-1" /\ acceptanceWrites = 1
SameKeyReplayReturnsStoredReceipt == replayResult = "original receipt" =>
  selectedQualification = acceptedQualification /\ selectedReplicas = acceptedReplicas
ChangedQualificationConflicts == replayResult = "conflict" =>
  selectedQualification # acceptedQualification \/ selectedReplicas # acceptedReplicas
ReplayDoesNotRereadSource == replayResult # "none" => sourceReads = 1
OriginalReplayUsesAcceptedRecord ==
  replayResult = "original receipt" => acceptedQualification = QualificationID /\ selectedReplicas = acceptedReplicas /\ sourceReads = 1
AcceptanceWriteIsUnique == acceptanceWrites <= 1
LostResponseLeavesDurableReceipt ==
  ~initialResponseObserved /\ durableReceipt # "none" => keyBound /\ acceptanceWrites = 1
ReplayUsesDurableReceipt == replayResult = "original receipt" => durableReceipt = "receipt-1" /\ keyBound
ReplayHydratesRestartedController == replayResult = "original receipt" => controllerAlive /\ processLocalAcceptance

THEOREM Spec => []TypeOK
====================================================
