-------------------- MODULE OnlineUpgrade --------------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Old, Target, Other, None
ASSUME Old # Target
ASSUME Other \notin {Old, Target, None}
ASSUME None \notin {Old, Target}

Phases == {"plan", "prepared", "expanded", "candidate-starting",
           "candidate-ready", "candidate-verified", "target-serving", "old-retired",
           "contracted", "done", "held"}
Schemas == {"old", "expanded", "contracted"}

VARIABLES phase, heldPhase, requestedTarget, journalTarget, schema,
          oldRunning, oldReady, targetRunning, targetReady, serving,
          writer, backupOK, shadowOK, oldWriterFenced, cutoverProof,
          targetHasWritten, receipt

vars == <<phase, heldPhase, requestedTarget, journalTarget, schema,
          oldRunning, oldReady, targetRunning, targetReady, serving,
          writer, backupOK, shadowOK, oldWriterFenced, cutoverProof,
          targetHasWritten, receipt>>

Init ==
  /\ phase = "plan"
  /\ heldPhase = "plan"
  /\ requestedTarget = Target
  /\ journalTarget = None
  /\ schema = "old"
  /\ oldRunning = TRUE
  /\ oldReady = TRUE
  /\ targetRunning = FALSE
  /\ targetReady = FALSE
  /\ serving = Old
  /\ writer = Old
  /\ backupOK = FALSE
  /\ shadowOK = FALSE
  /\ oldWriterFenced = FALSE
  /\ cutoverProof = FALSE
  /\ targetHasWritten = FALSE
  /\ receipt = FALSE

PrepareBackupAndShadow ==
  /\ phase = "plan"
  /\ backupOK' = TRUE
  /\ shadowOK' = TRUE
  /\ phase' = "prepared"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady,
                  serving, writer, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

BindIntent ==
  /\ phase = "prepared"
  /\ backupOK /\ shadowOK
  /\ requestedTarget = Target
  /\ journalTarget' = requestedTarget
  /\ phase' = "expanded"
  /\ UNCHANGED <<heldPhase, requestedTarget, schema, oldRunning, oldReady,
                  targetRunning, targetReady, serving, writer, backupOK,
                  shadowOK, oldWriterFenced, cutoverProof, targetHasWritten,
                  receipt>>

ExpandSchemaOnline ==
  /\ phase = "expanded"
  /\ journalTarget = Target
  /\ backupOK /\ shadowOK
  /\ schema' = "expanded"
  /\ phase' = "candidate-starting"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, oldRunning,
                  oldReady, targetRunning, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

StartPassiveCandidate ==
  /\ phase = "candidate-starting"
  /\ schema = "expanded"
  /\ journalTarget = Target
  /\ targetRunning' = TRUE
  /\ phase' = "candidate-ready"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

VerifyCandidate ==
  /\ phase = "candidate-ready"
  /\ targetRunning
  /\ schema = "expanded"
  /\ targetReady' = TRUE
  /\ phase' = "candidate-verified"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

CandidateFailsBeforeCutover ==
  /\ phase \in {"candidate-ready", "candidate-verified"}
  /\ targetRunning
  /\ targetReady' = FALSE
  /\ targetRunning' = FALSE
  /\ phase' = "held"
  /\ heldPhase' = "candidate-starting"
  /\ UNCHANGED <<requestedTarget, journalTarget, schema, oldRunning,
                  oldReady, serving, writer, backupOK, shadowOK,
                  oldWriterFenced, cutoverProof, targetHasWritten, receipt>>

AtomicWriterAndTrafficHandoff ==
  /\ phase = "candidate-verified"
  /\ targetReady /\ oldRunning /\ oldReady
  /\ journalTarget = Target
  /\ serving' = Target
  /\ writer' = Target
  /\ oldWriterFenced' = TRUE
  /\ phase' = "target-serving"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady,
                  backupOK, shadowOK, cutoverProof, targetHasWritten,
                  receipt>>

RecordTargetWrite ==
  /\ phase = "target-serving"
  /\ serving = Target /\ writer = Target /\ oldWriterFenced
  /\ targetHasWritten' = TRUE
  /\ UNCHANGED <<phase, heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady, serving,
                  writer, backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  receipt>>

VerifyCutover ==
  /\ phase = "target-serving"
  /\ serving = Target /\ writer = Target
  /\ targetReady /\ targetRunning
  /\ cutoverProof' = TRUE
  /\ UNCHANGED <<phase, heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady, serving,
                  writer, backupOK, shadowOK, oldWriterFenced, targetHasWritten,
                  receipt>>

RetireOldWriter ==
  /\ phase = "target-serving"
  /\ cutoverProof /\ targetReady /\ targetHasWritten
  /\ oldRunning' = FALSE
  /\ oldReady' = FALSE
  /\ phase' = "old-retired"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  targetRunning, targetReady, serving, writer, backupOK,
                  shadowOK, oldWriterFenced, cutoverProof, targetHasWritten,
                  receipt>>

ContractSchema ==
  /\ phase = "old-retired"
  /\ ~oldRunning /\ writer = Target /\ serving = Target
  /\ cutoverProof /\ targetHasWritten
  /\ schema' = "contracted"
  /\ phase' = "contracted"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, oldRunning,
                  oldReady, targetRunning, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

WriteTerminalReceipt ==
  /\ phase = "contracted"
  /\ schema = "contracted" /\ targetReady /\ serving = Target
  /\ receipt' = TRUE
  /\ phase' = "done"
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady, serving,
                  writer, backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten>>

ControllerCrashOrAmbiguousEffect ==
  /\ phase \in Phases \ {"done", "held"}
  /\ phase' = "held"
  /\ heldPhase' = phase
  /\ UNCHANGED <<requestedTarget, journalTarget, schema, oldRunning,
                  oldReady, targetRunning, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

ResumeSameIntent ==
  /\ phase = "held"
  /\ requestedTarget = Target
  /\ journalTarget \in {None, Target}
  /\ phase' = heldPhase
  /\ UNCHANGED <<heldPhase, requestedTarget, journalTarget, schema,
                  oldRunning, oldReady, targetRunning, targetReady, serving,
                  writer, backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

ChangedIntentStaysHeld ==
  /\ phase = "held"
  /\ journalTarget = Target
  /\ requestedTarget' = Other
  /\ UNCHANGED <<phase, heldPhase, journalTarget, schema, oldRunning,
                  oldReady, targetRunning, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

RetryBoundIntent ==
  /\ phase = "held"
  /\ journalTarget = Target
  /\ requestedTarget = Other
  /\ requestedTarget' = journalTarget
  /\ UNCHANGED <<phase, heldPhase, journalTarget, schema, oldRunning,
                  oldReady, targetRunning, targetReady, serving, writer,
                  backupOK, shadowOK, oldWriterFenced, cutoverProof,
                  targetHasWritten, receipt>>

HeldWait == /\ phase = "held" /\ UNCHANGED vars
DoneWait == /\ phase = "done" /\ UNCHANGED vars

Next == PrepareBackupAndShadow \/ BindIntent \/ ExpandSchemaOnline \/
        StartPassiveCandidate \/ VerifyCandidate \/
        CandidateFailsBeforeCutover \/ AtomicWriterAndTrafficHandoff \/ RecordTargetWrite \/ VerifyCutover \/
        RetireOldWriter \/ ContractSchema \/ WriteTerminalReceipt \/
        ControllerCrashOrAmbiguousEffect \/ ResumeSameIntent \/
        ChangedIntentStaysHeld \/ RetryBoundIntent \/ HeldWait \/ DoneWait

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in Phases
  /\ heldPhase \in Phases
  /\ requestedTarget \in {Target, Other}
  /\ journalTarget \in {None, Target}
  /\ schema \in Schemas
  /\ oldRunning \in BOOLEAN /\ oldReady \in BOOLEAN
  /\ targetRunning \in BOOLEAN /\ targetReady \in BOOLEAN
  /\ serving \in {Old, Target}
  /\ writer \in {Old, Target}
  /\ backupOK \in BOOLEAN /\ shadowOK \in BOOLEAN
  /\ oldWriterFenced \in BOOLEAN /\ cutoverProof \in BOOLEAN
  /\ targetHasWritten \in BOOLEAN /\ receipt \in BOOLEAN

OnlineAvailability ==
  (serving = Old /\ oldRunning /\ oldReady) \/
  (serving = Target /\ targetRunning /\ targetReady)

SingleServingWriter ==
  /\ (serving = Old => writer = Old)
  /\ (serving = Target => writer = Target)
  /\ writer \in {Old, Target}

NoContractWhileOldWriterRuns ==
  schema = "contracted" => ~oldRunning

OldBinaryCanReadSchema ==
  oldRunning => schema \in {"old", "expanded"}

TargetMustBeCompatibleBeforeServing ==
  serving = Target =>
    (targetReady /\ targetRunning /\ schema \in {"expanded", "contracted"} /\ journalTarget = Target)

TargetWritesRequireOldWriterFenced ==
  targetHasWritten => (writer = Target /\ serving = Target /\ oldWriterFenced)

TargetWriterRequiresOldFence ==
  writer = Target => oldWriterFenced

NoRollbackAfterTargetWrite ==
  targetHasWritten => (writer = Target /\ serving = Target)

ChangedIntentRemainsHeld ==
  journalTarget = Target /\ requestedTarget # journalTarget => phase = "held"

ReceiptRequiresVerifiedTerminalState ==
  receipt => (phase = "done" /\ schema = "contracted" /\ targetHasWritten /\ cutoverProof /\ ~oldRunning)

ContractedSchemaRequiresHealthyTarget ==
  schema = "contracted" => (targetReady /\ targetRunning /\ serving = Target /\ writer = Target)

===============================================================
