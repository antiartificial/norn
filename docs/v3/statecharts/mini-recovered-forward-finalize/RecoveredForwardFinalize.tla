-------------------- MODULE RecoveredForwardFinalize --------------------
EXTENDS Naturals, TLC

CONSTANTS AdoptionRank, TransitionSourceRank, CandidateRank,
          CandidateExact, Schema52, NormalHealthAvailable
ASSUME AdoptionRank \in Nat
ASSUME TransitionSourceRank \in Nat
ASSUME CandidateRank \in Nat
ASSUME CandidateExact \in BOOLEAN
ASSUME Schema52 \in BOOLEAN
ASSUME NormalHealthAvailable \in BOOLEAN

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
          preservationVerified, normalAPIHealthy, receipt

vars == <<phase, heldPhase, apiMode, hostAgent, writeFence,
          preservationVerified, normalAPIHealthy, receipt>>

Init ==
  /\ phase = "preservation-pending"
  /\ heldPhase = "preservation-pending"
  /\ apiMode = "maintenance"
  /\ hostAgent = "fenced"
  /\ writeFence = TRUE
  /\ preservationVerified = TRUE
  /\ normalAPIHealthy = FALSE
  /\ receipt = FALSE

HoldUnqualifiedCandidate ==
  /\ phase = "preservation-pending"
  /\ (~CandidateExact \/ ~Schema52)
  /\ phase' = "candidate-evidence-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

HoldUnprovenLineage ==
  /\ phase = "preservation-pending"
  /\ CandidateExact /\ Schema52 /\ ~SignedLineageVerified
  /\ phase' = "finalization-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

RequestFinalize ==
  /\ phase = "preservation-pending"
  /\ CandidateExact /\ Schema52 /\ preservationVerified
  /\ SignedLineageVerified
  /\ phase' = "finalize-requested"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

MiniAcceptsFinalization ==
  /\ phase = "finalize-requested"
  /\ writeFence /\ preservationVerified
  /\ SignedLineageVerified
  /\ writeFence' = FALSE
  /\ phase' = "mutation-fence-released"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent,
                  preservationVerified, normalAPIHealthy, receipt>>

StopMaintenanceAPI ==
  /\ phase = "mutation-fence-released"
  /\ apiMode = "maintenance"
  /\ apiMode' = "stopped"
  /\ phase' = "normal-activation-pending"
  /\ UNCHANGED <<heldPhase, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

StartNormalAPI ==
  /\ phase = "normal-activation-pending"
  /\ apiMode = "stopped"
  /\ CandidateExact /\ Schema52
  /\ apiMode' = "normal"
  /\ phase' = "normal-api-active-pending-agent"
  /\ UNCHANGED <<heldPhase, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

HoldOnWrongCandidate ==
  /\ phase = "normal-activation-pending"
  /\ apiMode = "stopped"
  /\ (~CandidateExact \/ ~Schema52)
  /\ phase' = "candidate-evidence-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

VerifyNormalAPI ==
  /\ phase = "normal-api-active-pending-agent"
  /\ apiMode = "normal"
  /\ NormalHealthAvailable
  /\ normalAPIHealthy' = TRUE
  /\ phase' = "normal-api-verified"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, receipt>>

HoldOnNormalHealthFailure ==
  /\ phase = "normal-api-active-pending-agent"
  /\ apiMode = "normal"
  /\ ~NormalHealthAvailable
  /\ phase' = "normal-api-held"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

PersistHostAgentRestartIntent ==
  /\ phase = "normal-api-verified"
  /\ normalAPIHealthy /\ apiMode = "normal"
  /\ hostAgent = "fenced"
  /\ phase' = "host-agent-restart-pending"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

RestartHostAgent ==
  /\ phase = "host-agent-restart-pending"
  /\ normalAPIHealthy /\ apiMode = "normal"
  /\ hostAgent = "fenced"
  /\ hostAgent' = "active"
  /\ phase' = "host-agent-active"
  /\ UNCHANGED <<heldPhase, apiMode, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

WriteTerminalReceipt ==
  /\ phase = "host-agent-active"
  /\ apiMode = "normal" /\ hostAgent = "active"
  /\ normalAPIHealthy /\ preservationVerified
  /\ SignedLineageVerified /\ CandidateExact /\ Schema52
  /\ receipt' = TRUE
  /\ phase' = "done"
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy>>

CrashOrUncertainEffect ==
  /\ phase \in Phases \ {"done", "held", "candidate-evidence-held", "finalization-held", "normal-api-held"}
  /\ phase' = "held"
  /\ heldPhase' = phase
  /\ UNCHANGED <<apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

ResumeSameTransition ==
  /\ phase = "held"
  /\ heldPhase \in Phases \ {"held", "done", "candidate-evidence-held", "finalization-held", "normal-api-held"}
  /\ phase' = heldPhase
  /\ UNCHANGED <<heldPhase, apiMode, hostAgent, writeFence,
                  preservationVerified, normalAPIHealthy, receipt>>

HeldWait == /\ phase = "held" /\ UNCHANGED vars
CandidateEvidenceWait == /\ phase = "candidate-evidence-held" /\ UNCHANGED vars
LineageWait == /\ phase = "finalization-held" /\ UNCHANGED vars
NormalHealthWait == /\ phase = "normal-api-held" /\ UNCHANGED vars
DoneWait == /\ phase = "done" /\ UNCHANGED vars

Next == HoldUnqualifiedCandidate \/ HoldUnprovenLineage \/ RequestFinalize \/
        MiniAcceptsFinalization \/ StopMaintenanceAPI \/ StartNormalAPI \/
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
  /\ normalAPIHealthy \in BOOLEAN
  /\ receipt \in BOOLEAN

WriteFenceReleaseRequiresSignedEvidence ==
  ~writeFence => (SignedLineageVerified /\ preservationVerified)

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
              CandidateExact /\ Schema52)

=============================================================================
