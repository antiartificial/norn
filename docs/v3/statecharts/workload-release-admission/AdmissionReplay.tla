---------------- MODULE AdmissionReplay ----------------
EXTENDS Naturals, TLC

CONSTANTS QualificationID, AllowedReplicas, OutOfPolicyReplicas

VARIABLES phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
          qualificationValid, sourceAvailable, sourceReads, replayResult
vars == <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
          qualificationValid, sourceAvailable, sourceReads, replayResult>>

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

AcceptVerifiedQualification ==
  /\ phase = "request"
  /\ ~keyBound
  /\ qualificationValid
  /\ sourceAvailable
  /\ selectedReplicas \in AllowedReplicas
  /\ keyBound' = TRUE
  /\ acceptedQualification' = selectedQualification
  /\ acceptedReplicas' = selectedReplicas
  /\ sourceReads' = sourceReads + 1
  /\ phase' = "accepted"
  /\ UNCHANGED <<selectedQualification, selectedReplicas, qualificationValid, sourceAvailable, replayResult>>

RejectOutOfPolicySize ==
  /\ phase = "request"
  /\ selectedReplicas = OutOfPolicyReplicas
  /\ phase' = "rejected"
  /\ UNCHANGED <<selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, replayResult>>

QualificationExpires ==
  /\ keyBound
  /\ qualificationValid
  /\ qualificationValid' = FALSE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  sourceAvailable, sourceReads, replayResult>>

AcceptedSourceDisappears ==
  /\ keyBound
  /\ sourceAvailable
  /\ sourceAvailable' = FALSE
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceReads, replayResult>>

RetrySameQualification ==
  /\ keyBound
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ replayResult = "none"
  /\ replayResult' = "original receipt"
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads>>

SelectDifferentQualification ==
  /\ keyBound
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ replayResult = "none"
  /\ selectedQualification' = "different-qualification"
  /\ UNCHANGED <<phase, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound, qualificationValid,
                  sourceAvailable, sourceReads, replayResult>>

SelectDifferentReplicaCount ==
  /\ keyBound
  /\ selectedQualification = acceptedQualification
  /\ selectedReplicas = acceptedReplicas
  /\ 3 \in AllowedReplicas
  /\ replayResult = "none"
  /\ selectedReplicas' = 3
  /\ UNCHANGED <<phase, selectedQualification, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, replayResult>>

SelectOutOfPolicyReplicaCount ==
  /\ phase = "request"
  /\ selectedReplicas \in AllowedReplicas
  /\ selectedReplicas' = OutOfPolicyReplicas
  /\ UNCHANGED <<phase, selectedQualification, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads, replayResult>>

RetryChangedQualification ==
  /\ keyBound
  /\ (selectedQualification # acceptedQualification \/ selectedReplicas # acceptedReplicas)
  /\ replayResult = "none"
  /\ replayResult' = "conflict"
  /\ UNCHANGED <<phase, selectedQualification, selectedReplicas, acceptedQualification, acceptedReplicas, keyBound,
                  qualificationValid, sourceAvailable, sourceReads>>

Done == (replayResult # "none" \/ phase = "rejected") /\ UNCHANGED vars

Next == AcceptVerifiedQualification \/ RejectOutOfPolicySize \/ SelectOutOfPolicyReplicaCount
        \/ QualificationExpires \/ AcceptedSourceDisappears
        \/ RetrySameQualification \/ SelectDifferentQualification \/ SelectDifferentReplicaCount
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

AcceptanceHasVerifiedSource ==
  keyBound => acceptedQualification = QualificationID /\ sourceReads = 1 /\ acceptedReplicas \in AllowedReplicas
SameKeyReplayReturnsStoredReceipt == replayResult = "original receipt" =>
  selectedQualification = acceptedQualification /\ selectedReplicas = acceptedReplicas
ChangedQualificationConflicts == replayResult = "conflict" =>
  selectedQualification # acceptedQualification \/ selectedReplicas # acceptedReplicas
ReplayDoesNotRereadSource == replayResult # "none" => sourceReads = 1
OriginalReplayUsesAcceptedRecord ==
  replayResult = "original receipt" => acceptedQualification = QualificationID /\ selectedReplicas = acceptedReplicas /\ sourceReads = 1

THEOREM Spec => []TypeOK
====================================================
