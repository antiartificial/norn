-------------------- MODULE MiniDirectUpgrade --------------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Old, Target, None, UnknownTarget
ASSUME Old # Target
ASSUME None \notin {Old, Target}
ASSUME UnknownTarget \notin {Old, Target, None}

Phases == {"plan", "backup", "prepared", "host-agent-fencing",
           "api-fencing", "legacy-fenced", "migration-started",
           "schema-migrated", "candidate-started", "candidate-healthy",
           "done", "held"}

VARIABLES phase, heldPhase, intentTarget, journalTarget, writerProof,
          schema, api, hostAgent, backupOK, shadowOK, writeFence,
          healthy, preservation, receipt

vars == <<phase, heldPhase, intentTarget, journalTarget, writerProof,
          schema, api, hostAgent, backupOK, shadowOK, writeFence,
          healthy, preservation, receipt>>

Init ==
  /\ phase = "plan"
  /\ heldPhase = "plan"
  /\ intentTarget = Target
  /\ journalTarget = None
  /\ writerProof = "unknown"
  /\ schema = 49
  /\ api = Old
  /\ hostAgent = Old
  /\ backupOK = FALSE
  /\ shadowOK = FALSE
  /\ writeFence = FALSE
  /\ healthy = FALSE
  /\ preservation = FALSE
  /\ receipt = FALSE

ProveWriters ==
  /\ phase = "backup"
  /\ writerProof = "unknown"
  /\ phase' = "prepared"
  /\ writerProof' = "all-known-and-compatible-or-fenceable"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

HoldUnknownWriters ==
  /\ phase = "backup"
  /\ writerProof = "unknown"
  /\ phase' = "held"
  /\ heldPhase' = "backup"
  /\ UNCHANGED <<intentTarget, journalTarget, writerProof, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

RefreshWriterProof ==
  /\ phase = "held"
  /\ heldPhase = "backup"
  /\ writerProof = "unknown"
  /\ writerProof' = "all-known-and-compatible-or-fenceable"
  /\ phase' = "prepared"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

TakeBackupAndShadow ==
  /\ phase = "plan"
  /\ backupOK' = TRUE
  /\ shadowOK' = TRUE
  /\ phase' = "backup"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, hostAgent, writeFence, healthy,
                  preservation, receipt>>

BindJournal ==
  /\ phase = "prepared"
  /\ backupOK /\ shadowOK
  /\ writerProof = "all-known-and-compatible-or-fenceable"
  /\ journalTarget' = intentTarget
  /\ phase' = "host-agent-fencing"
  /\ UNCHANGED <<heldPhase, intentTarget, writerProof, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

FenceHostAgent ==
  /\ phase = "host-agent-fencing"
  /\ journalTarget = intentTarget
  /\ hostAgent' = None
  /\ phase' = "api-fencing"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

FenceAPI ==
  /\ phase = "api-fencing"
  /\ hostAgent = None
  /\ api' = None
  /\ writeFence' = TRUE
  /\ phase' = "legacy-fenced"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, hostAgent, backupOK, shadowOK, healthy,
                  preservation, receipt>>

BeginMigration ==
  /\ phase = "legacy-fenced"
  /\ api = None /\ hostAgent = None /\ writeFence
  /\ phase' = "migration-started"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, hostAgent, backupOK, shadowOK, writeFence,
                  healthy, preservation, receipt>>

FinishMigration ==
  /\ phase = "migration-started"
  /\ writeFence /\ api = None /\ hostAgent = None
  /\ schema' = 51
  /\ phase' = "schema-migrated"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  api, hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

StartCandidate ==
  /\ phase = "schema-migrated"
  /\ schema = 51 /\ shadowOK /\ journalTarget = Target
  /\ api' = Target
  /\ hostAgent' = Target
  /\ phase' = "candidate-started"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

VerifyCandidateAndPreservation ==
  /\ phase = "candidate-started"
  /\ api = Target /\ hostAgent = Target /\ schema = 51
  /\ healthy' = TRUE
  /\ preservation' = TRUE
  /\ phase' = "candidate-healthy"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, hostAgent, backupOK, shadowOK, writeFence,
                  receipt>>

Finalize ==
  /\ phase = "candidate-healthy"
  /\ healthy /\ preservation /\ api = Target /\ hostAgent = Target
  /\ receipt' = TRUE
  /\ writeFence' = FALSE
  /\ phase' = "done"
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, hostAgent, backupOK, shadowOK, healthy,
                  preservation>>

CrashOrAmbiguousOutcome ==
  /\ phase \in Phases \ {"done", "held"}
  /\ phase' = "held"
  /\ heldPhase' = phase
  /\ UNCHANGED <<intentTarget, journalTarget, writerProof, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

ResumeSameIntent ==
  /\ phase = "held"
  /\ intentTarget = Target
  /\ journalTarget \in {None, Target}
  /\ phase' = heldPhase
  /\ UNCHANGED <<heldPhase, intentTarget, journalTarget, writerProof,
                  schema, api, hostAgent, backupOK, shadowOK, writeFence,
                  healthy, preservation, receipt>>

TryDifferentTarget ==
  /\ phase = "held"
  /\ journalTarget = Target
  /\ intentTarget' = UnknownTarget
  /\ phase' = "held"
  /\ UNCHANGED <<heldPhase, journalTarget, writerProof, schema, api,
                  hostAgent, backupOK, shadowOK, writeFence, healthy,
                  preservation, receipt>>

HeldWait == /\ phase = "held" /\ UNCHANGED vars
DoneWait == /\ phase = "done" /\ UNCHANGED vars

Next == ProveWriters \/ HoldUnknownWriters \/ RefreshWriterProof \/
        TakeBackupAndShadow \/ BindJournal \/ FenceHostAgent \/ FenceAPI \/
        BeginMigration \/ FinishMigration \/ StartCandidate \/
        VerifyCandidateAndPreservation \/ Finalize \/ CrashOrAmbiguousOutcome \/
        ResumeSameIntent \/ TryDifferentTarget \/ HeldWait \/ DoneWait

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in Phases
  /\ heldPhase \in Phases
  /\ intentTarget \in {Target, UnknownTarget}
  /\ journalTarget \in {None, Target}
  /\ writerProof \in {"unknown", "all-known-and-compatible-or-fenceable"}
  /\ schema \in {49, 51}
  /\ api \in {Old, Target, None}
  /\ hostAgent \in {Old, Target, None}
  /\ backupOK \in BOOLEAN /\ shadowOK \in BOOLEAN
  /\ writeFence \in BOOLEAN /\ healthy \in BOOLEAN
  /\ preservation \in BOOLEAN /\ receipt \in BOOLEAN

MigrationRequiresFencedWriters ==
  phase \in {"migration-started", "schema-migrated"} =>
    (api = None /\ hostAgent = None /\ writeFence)

NoOldWriterAfterSchemaChange ==
  schema = 51 => (api # Old /\ hostAgent # Old)

TargetRequiresBoundVerifiedCandidate ==
  api = Target => (shadowOK /\ journalTarget = Target /\ schema = 51)

ReceiptRequiresSuccessfulPostflight ==
  receipt => (phase = "done" /\ healthy /\ preservation /\ api = Target /\ hostAgent = Target)

ChangedIntentStaysHeld ==
  journalTarget = Target /\ intentTarget # journalTarget => phase = "held"

=============================================================================
