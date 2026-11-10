-------------------- MODULE RecoveredForwardFinalize --------------------
EXTENDS Naturals, TLC

CONSTANTS AdoptionRank, TransitionSourceRank, CandidateRank,
          CandidateExact, Schema52, NormalHealthAvailable,
          PriorControllerVerified
ASSUME AdoptionRank \in Nat
ASSUME TransitionSourceRank \in Nat
ASSUME CandidateRank \in Nat
ASSUME CandidateExact \in BOOLEAN
ASSUME Schema52 \in BOOLEAN
ASSUME NormalHealthAvailable \in BOOLEAN
ASSUME PriorControllerVerified \in BOOLEAN

SignedLineageVerified ==
  AdoptionRank <= TransitionSourceRank
  /\ TransitionSourceRank <= CandidateRank

Phases == {"preservation-pending", "candidate-evidence-held",
           "finalization-held", "finalize-requested",
           "mutation-fence-released", "normal-activation-pending",
           "normal-api-active-pending-agent", "normal-api-verified",
           "normal-api-held", "host-agent-restart-pending",
           "host-agent-active", "done", "held"}

VARIABLES phase, heldPhase, apiMode, hostAgent, writeFence,
          preservationVerified, preservationControllerVerified,
          recordController, plistController, apiController,
          normalAPIHealthy, receipt

vars == <<phase, heldPhase, apiMode, hostAgent, writeFence,
          preservationVerified, preservationControllerVerified,
          recordController, plistController, apiController,
          normalAPIHealthy, receipt>>

Init ==
  /\ phase = "preservation-pending"
  /\ heldPhase = "preservation-pending"
  /\ apiMode = "maintenance"
  /\ hostAgent = "fenced"
  /\ writeFence = TRUE
  /\ preservationVerified = TRUE
  /\ preservationControllerVerified = FALSE
  /\ recordController = "prior"
  /\ plistController = "prior"
  /\ apiController = "prior"
  /\ normalAPIHealthy = FALSE
  /\ receipt = FALSE

HoldUnqualifiedCandidate ==
  /\ phase = "preservation-pending"
  /\ (~CandidateExact \/ ~Schema52)
  /\ phase' = "candidate-evidence-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

HoldUnprovenLineage ==
  /\ phase = "preservation-pending"
  /\ CandidateExact /\ Schema52 /\ ~SignedLineageVerified
  /\ phase' = "finalization-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

VerifyPriorControllerBinding ==
  /\ phase = "preservation-pending"
  /\ recordController = "prior" /\ plistController = "prior"
  /\ apiController = "prior" /\ PriorControllerVerified
  /\ preservationControllerVerified' = TRUE
  /\ UNCHANGED <<phase, heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, recordController, plistController,
                  apiController, normalAPIHealthy, receipt>>

HoldUnverifiedController ==
  /\ phase = "preservation-pending"
  /\ ~PriorControllerVerified
  /\ phase' = "finalization-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

RequestFinalize ==
  /\ phase = "preservation-pending"
  /\ CandidateExact /\ Schema52 /\ preservationVerified
  /\ preservationControllerVerified
  /\ SignedLineageVerified
  /\ phase' = "finalize-requested"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

MiniAcceptsFinalization ==
  /\ phase = "finalize-requested"
  /\ writeFence /\ preservationVerified
  /\ SignedLineageVerified
  /\ writeFence' = FALSE
  /\ phase' = "mutation-fence-released"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

EnterNormalActivationPending ==
  /\ phase = "mutation-fence-released"
  /\ phase' = "normal-activation-pending"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

StageNormalControllerPlist ==
  /\ phase = "normal-activation-pending"
  /\ plistController \in {"prior", "finalizer"}
  /\ plistController' = "finalizer"
  /\ UNCHANGED <<phase, heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, apiController, normalAPIHealthy, receipt>>

RebindSignedControllerRecord ==
  /\ phase = "normal-activation-pending"
  /\ plistController = "finalizer"
  /\ recordController \in {"prior", "finalizer"}
  /\ recordController' = "finalizer"
  /\ UNCHANGED <<phase, heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  plistController, apiController, normalAPIHealthy, receipt>>

StopMaintenanceAPI ==
  /\ phase = "normal-activation-pending"
  /\ apiMode = "maintenance"
  /\ recordController = "finalizer" /\ plistController = "finalizer"
  /\ apiController \in {"prior", "finalizer"}
  /\ apiMode' = "stopped"
  /\ apiController' = "none"
  /\ UNCHANGED <<phase, heldPhase, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, normalAPIHealthy, receipt>>

StartNormalAPI ==
  /\ phase = "normal-activation-pending"
  /\ apiMode = "stopped"
  /\ recordController = "finalizer" /\ plistController = "finalizer"
  /\ CandidateExact /\ Schema52
  /\ apiMode' = "normal"
  /\ apiController' = "finalizer"
  /\ phase' = "normal-api-active-pending-agent"
  /\ UNCHANGED <<heldPhase, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, normalAPIHealthy, receipt>>

HoldOnWrongCandidate ==
  /\ phase = "normal-activation-pending"
  /\ apiMode = "stopped"
  /\ (~CandidateExact \/ ~Schema52)
  /\ phase' = "candidate-evidence-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

VerifyNormalAPI ==
  /\ phase = "normal-api-active-pending-agent"
  /\ apiMode = "normal"
  /\ NormalHealthAvailable
  /\ normalAPIHealthy' = TRUE
  /\ phase' = "normal-api-verified"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController, receipt>>

HoldOnNormalHealthFailure ==
  /\ phase = "normal-api-active-pending-agent"
  /\ apiMode = "normal"
  /\ ~NormalHealthAvailable
  /\ phase' = "normal-api-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

PersistHostAgentRestartIntent ==
  /\ phase = "normal-api-verified"
  /\ normalAPIHealthy /\ apiMode = "normal"
  /\ hostAgent = "fenced"
  /\ phase' = "host-agent-restart-pending"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

RestartHostAgent ==
  /\ phase = "host-agent-restart-pending"
  /\ normalAPIHealthy /\ apiMode = "normal"
  /\ hostAgent = "fenced"
  /\ hostAgent' = "active"
  /\ phase' = "host-agent-active"
  /\ UNCHANGED <<heldPhase, apiMode, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

WriteTerminalReceipt ==
  /\ phase = "host-agent-active"
  /\ apiMode = "normal" /\ hostAgent = "active"
  /\ normalAPIHealthy /\ preservationVerified
  /\ SignedLineageVerified /\ CandidateExact /\ Schema52
  /\ receipt' = TRUE
  /\ phase' = "done"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy>>

CrashOrUncertainEffect ==
  /\ phase \in Phases \ {"done", "held", "candidate-evidence-held", "finalization-held", "normal-api-held"}
  /\ phase' = "held"
  /\ heldPhase' = phase
  /\ UNCHANGED <<apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

ResumeSameTransition ==
  /\ phase = "held"
  /\ heldPhase \in Phases \ {"held", "done", "candidate-evidence-held", "finalization-held", "normal-api-held"}
  /\ phase' = heldPhase
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, preservationControllerVerified,
                  recordController, plistController, apiController,
                  normalAPIHealthy, receipt>>

HeldWait == /\ phase = "held" /\ UNCHANGED vars
CandidateEvidenceWait == /\ phase = "candidate-evidence-held" /\ UNCHANGED vars
LineageWait == /\ phase = "finalization-held" /\ UNCHANGED vars
NormalHealthWait == /\ phase = "normal-api-held" /\ UNCHANGED vars
DoneWait == /\ phase = "done" /\ UNCHANGED vars

Next == HoldUnqualifiedCandidate \/ HoldUnprovenLineage \/ RequestFinalize \/
        VerifyPriorControllerBinding \/ HoldUnverifiedController \/
        MiniAcceptsFinalization \/ EnterNormalActivationPending \/
        StageNormalControllerPlist \/ RebindSignedControllerRecord \/
        StopMaintenanceAPI \/ StartNormalAPI \/
        HoldOnWrongCandidate \/ VerifyNormalAPI \/ HoldOnNormalHealthFailure \/
        PersistHostAgentRestartIntent \/ RestartHostAgent \/ WriteTerminalReceipt \/
        CrashOrUncertainEffect \/ ResumeSameTransition \/ HeldWait \/
        CandidateEvidenceWait \/ LineageWait \/ NormalHealthWait \/ DoneWait

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in Phases
  /\ heldPhase \in Phases
  /\ apiMode \in {"maintenance", "stopped", "normal"}
  /\ hostAgent \in {"fenced", "active"}
  /\ writeFence \in BOOLEAN
  /\ preservationVerified \in BOOLEAN
  /\ preservationControllerVerified \in BOOLEAN
  /\ recordController \in {"prior", "finalizer"}
  /\ plistController \in {"prior", "finalizer"}
  /\ apiController \in {"prior", "finalizer", "none"}
  /\ normalAPIHealthy \in BOOLEAN
  /\ receipt \in BOOLEAN

WriteFenceReleaseRequiresSignedEvidence ==
  ~writeFence => (SignedLineageVerified /\ preservationVerified /\ preservationControllerVerified)

FinalizationHoldKeepsOriginalFences ==
  phase = "finalization-held" =>
    (writeFence /\ apiMode = "maintenance" /\ hostAgent = "fenced" /\ ~receipt)

CandidateEvidenceHoldKeepsAgentFenced ==
  phase = "candidate-evidence-held" => hostAgent = "fenced" /\ ~receipt

AgentActivationRequiresVerifiedNormalAPI ==
  hostAgent = "active" => (apiMode = "normal" /\ normalAPIHealthy /\ SignedLineageVerified)

TerminalReceiptRequiresFullReadback ==
  receipt => (phase = "done" /\ apiMode = "normal" /\ hostAgent = "active" /\
              normalAPIHealthy /\ preservationVerified /\ SignedLineageVerified /\
              preservationControllerVerified /\ recordController = "finalizer" /\
              plistController = "finalizer" /\ apiController = "finalizer" /\
              CandidateExact /\ Schema52)

NormalAPIRequiresReboundController ==
  apiMode = "normal" => (recordController = "finalizer" /\
                         plistController = "finalizer" /\
                         apiController = "finalizer")

=============================================================================
