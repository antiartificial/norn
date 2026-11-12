-------------------------- MODULE FreshFleetNornAuthority --------------------------
EXTENDS Naturals, TLC

MaxNodes == 9

VARIABLES phase, intent, dispatchBound, attempt, active, admissionAttempt,
          grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
          providerNodes, readbackNodes, terminalReceiptRecorded, targetRegistered

vars == <<phase, intent, dispatchBound, attempt, active, admissionAttempt,
         grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
         providerNodes, readbackNodes, terminalReceiptRecorded, targetRegistered>>

Init ==
    /\ phase = "unbound"
    /\ intent = "none"
    /\ dispatchBound = FALSE
    /\ attempt = 0
    /\ active = FALSE
    /\ admissionAttempt = 0
    /\ grantRemainder = 0
    /\ grantIssued = FALSE
    /\ grantConsumed = FALSE
    /\ consumeResponseLost = FALSE
    /\ providerNodes = 0
    /\ readbackNodes = 0
    /\ terminalReceiptRecorded = FALSE
    /\ targetRegistered = FALSE

BindIntent ==
    /\ phase = "unbound"
    /\ intent' = "fresh-fleet-1"
    /\ phase' = "intent-bound"
    /\ UNCHANGED <<dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

BindProtectedDispatch ==
    /\ phase = "intent-bound"
    /\ intent = "fresh-fleet-1"
    /\ ~dispatchBound
    /\ dispatchBound' = TRUE
    /\ phase' = "dispatch-bound"
    /\ UNCHANGED <<intent, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

StartInitialAttempt ==
    /\ phase = "dispatch-bound"
    /\ dispatchBound
    /\ attempt = 0
    /\ attempt' = 1
    /\ active' = TRUE
    /\ phase' = "attempt-running"
    /\ UNCHANGED <<intent, dispatchBound, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

RecordInitialAdmission ==
    /\ phase = "attempt-running"
    /\ attempt = 1
    /\ active
    /\ providerNodes = 0
    /\ readbackNodes = 0
    /\ admissionAttempt = 0
    /\ admissionAttempt' = attempt
    /\ phase' = "initial-admitted"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

ConsumeInitialGrant ==
    /\ phase = "initial-admitted"
    /\ attempt = 1
    /\ active
    /\ admissionAttempt = attempt
    /\ grantRemainder = 0
    /\ ~grantIssued
    /\ ~grantConsumed
    /\ grantRemainder' = MaxNodes
    /\ grantIssued' = TRUE
    /\ grantConsumed' = TRUE
    /\ consumeResponseLost' = FALSE
    /\ phase' = "initial-grant-consumed"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   admissionAttempt, providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

LoseInitialConsumeResponse ==
    /\ phase = "initial-grant-consumed"
    /\ grantIssued
    /\ grantConsumed
    /\ ~consumeResponseLost
    /\ consumeResponseLost' = TRUE
    /\ phase' = "initial-consume-response-lost"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed,
                   providerNodes, readbackNodes, terminalReceiptRecorded, targetRegistered>>

ReadBackConsumedInitialGrant ==
    /\ phase = "initial-consume-response-lost"
    /\ grantIssued
    /\ grantConsumed
    /\ consumeResponseLost
    /\ consumeResponseLost' = FALSE
    /\ phase' = "initial-grant-consumed"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed,
                   providerNodes, readbackNodes, terminalReceiptRecorded, targetRegistered>>

CreateEightLoseResponse ==
    /\ phase = "initial-grant-consumed"
    /\ grantIssued
    /\ grantConsumed
    /\ grantRemainder = MaxNodes
    /\ providerNodes = 0
    /\ providerNodes' \in {7, 8}
    /\ active' = FALSE
    /\ phase' = "response-lost"
    /\ UNCHANGED <<intent, dispatchBound, attempt, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   readbackNodes, terminalReceiptRecorded, targetRegistered>>

ReadBackExactRemainder ==
    /\ phase = "response-lost"
    /\ providerNodes = 8
    /\ readbackNodes' = providerNodes
    /\ phase' = "recovery-ready"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, terminalReceiptRecorded, targetRegistered>>

RecordExactRecoveryReadback ==
    /\ phase = "recovery-ready"
    /\ readbackNodes = 8
    /\ grantConsumed
    /\ phase' = "recovery-readback-recorded"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes, terminalReceiptRecorded, targetRegistered>>

StartSameIntentRecovery ==
    /\ phase = "recovery-readback-recorded"
    /\ readbackNodes = 8
    /\ grantConsumed
    /\ attempt' = attempt + 1
    /\ active' = TRUE
    /\ phase' = "recovery-running"
    /\ UNCHANGED <<intent, dispatchBound, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

RecordRecoveryAdmission ==
    /\ phase = "recovery-running"
    /\ attempt = 2
    /\ active
    /\ readbackNodes = 8
    /\ admissionAttempt = 1
    /\ admissionAttempt' = attempt
    /\ phase' = "recovery-admitted"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

ConsumeRemainderGrant ==
    /\ phase = "recovery-admitted"
    /\ attempt = 2
    /\ active
    /\ admissionAttempt = attempt
    /\ intent = "fresh-fleet-1"
    /\ grantRemainder = MaxNodes
    /\ grantIssued
    /\ grantConsumed
    /\ ~consumeResponseLost
    /\ readbackNodes = 8
    /\ grantRemainder' = 1
    /\ consumeResponseLost' = FALSE
    /\ phase' = "remainder-grant-consumed"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   admissionAttempt, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes,
                   terminalReceiptRecorded, targetRegistered>>

CreateNinth ==
    /\ phase = "remainder-grant-consumed"
    /\ grantRemainder = 1
    /\ providerNodes = 8
    /\ providerNodes' = 9
    /\ active' = FALSE
    /\ phase' = "complete-readback-needed"
    /\ UNCHANGED <<intent, dispatchBound, attempt, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   readbackNodes, terminalReceiptRecorded, targetRegistered>>

ReadBackAllNodes ==
    /\ phase = "complete-readback-needed"
    /\ providerNodes = MaxNodes
    /\ readbackNodes' = providerNodes
    /\ phase' = "target-registration-ready"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   admissionAttempt, grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes,
                   terminalReceiptRecorded, targetRegistered>>

RegisterTargetAndTransferFence ==
    /\ phase = "terminal-receipt-recorded"
    /\ readbackNodes = MaxNodes
    /\ terminalReceiptRecorded
    /\ ~targetRegistered
    /\ targetRegistered' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   admissionAttempt, grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes,
                   readbackNodes, terminalReceiptRecorded>>

RecordCompleteTerminalReceipt ==
    /\ phase = "target-registration-ready"
    /\ readbackNodes = MaxNodes
    /\ providerNodes = MaxNodes
    /\ ~terminalReceiptRecorded
    /\ terminalReceiptRecorded' = TRUE
    /\ phase' = "terminal-receipt-recorded"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active, admissionAttempt,
                   grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes, readbackNodes, targetRegistered>>

HoldUnknownReadback ==
    /\ phase = "response-lost"
    /\ providerNodes # 8
    /\ phase' = "held"
    /\ UNCHANGED <<intent, dispatchBound, attempt, active,
                   admissionAttempt, grantRemainder, grantIssued, grantConsumed, consumeResponseLost,
                   providerNodes,
                   readbackNodes, terminalReceiptRecorded, targetRegistered>>

Done == phase \in {"complete", "held"} /\ UNCHANGED vars

Next == BindIntent \/ BindProtectedDispatch \/ StartInitialAttempt \/
        RecordInitialAdmission \/ ConsumeInitialGrant \/ LoseInitialConsumeResponse \/
        ReadBackConsumedInitialGrant \/
        CreateEightLoseResponse \/ ReadBackExactRemainder \/
        RecordExactRecoveryReadback \/ StartSameIntentRecovery \/ RecordRecoveryAdmission \/
        ConsumeRemainderGrant \/ CreateNinth \/ ReadBackAllNodes \/
        RecordCompleteTerminalReceipt \/ RegisterTargetAndTransferFence \/ HoldUnknownReadback \/ Done

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ phase \in {"unbound", "intent-bound", "dispatch-bound",
                   "attempt-running", "initial-admitted",
                   "initial-grant-consumed", "initial-consume-response-lost", "response-lost",
                   "recovery-ready", "recovery-readback-recorded", "recovery-running",
                   "recovery-admitted", "remainder-grant-consumed",
                   "complete-readback-needed", "target-registration-ready", "terminal-receipt-recorded",
                   "complete", "held"}
    /\ intent \in {"none", "fresh-fleet-1"}
    /\ dispatchBound \in BOOLEAN
    /\ attempt \in 0..2
    /\ active \in BOOLEAN
    /\ admissionAttempt \in 0..2
    /\ grantRemainder \in 0..MaxNodes
    /\ grantIssued \in BOOLEAN
    /\ grantConsumed \in BOOLEAN
    /\ consumeResponseLost \in BOOLEAN
    /\ providerNodes \in 0..MaxNodes
    /\ readbackNodes \in 0..MaxNodes
    /\ terminalReceiptRecorded \in BOOLEAN
    /\ targetRegistered \in BOOLEAN

EffectNeedsConsumedGrant == providerNodes > 0 => grantIssued /\ grantConsumed /\ grantRemainder > 0
ConsumedGrantWasIssued == grantConsumed => grantIssued
GrantTracksExactRemainder ==
    phase = "remainder-grant-consumed" =>
        grantRemainder = MaxNodes - readbackNodes /\ admissionAttempt = attempt
AttemptNeedsProtectedDispatch == attempt > 0 => dispatchBound
AdmissionBelongsToCurrentGrant ==
    phase \in {"initial-grant-consumed", "remainder-grant-consumed"} =>
        admissionAttempt = attempt /\ grantRemainder = MaxNodes - readbackNodes
RecoveryKeepsIntent == attempt = 2 => intent = "fresh-fleet-1"
RegistrationNeedsTerminalReceipt == targetRegistered =>
    terminalReceiptRecorded /\ readbackNodes = MaxNodes /\ providerNodes = MaxNodes
NoTargetDuringCreate == providerNodes < MaxNodes => ~targetRegistered

=============================================================================
SPECIFICATION Spec
INVARIANT TypeOK
INVARIANT EffectNeedsConsumedGrant
INVARIANT ConsumedGrantWasIssued
INVARIANT GrantTracksExactRemainder
INVARIANT AttemptNeedsProtectedDispatch
INVARIANT AdmissionBelongsToCurrentGrant
INVARIANT RecoveryKeepsIntent
INVARIANT RegistrationNeedsTerminalReceipt
INVARIANT NoTargetDuringCreate
CHECK_DEADLOCK TRUE
