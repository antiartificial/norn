---------------- MODULE HeldReleaseAbandonment ----------------
EXTENDS Naturals, TLC

CONSTANTS AllowUnsafeAbandon, AllowUnsafeDelete, AllowUnsafeKeyRetire

VARIABLES phase, owner, inputPresent, inputTombstone, otherInputPresent,
          otherInputTombstone, completionReceipt, effectsReconciled,
          abandonAuthorized, abandonReceipt, keyReferenceProof, keyAvailable
vars == <<phase, owner, inputPresent, inputTombstone, otherInputPresent,
          otherInputTombstone, completionReceipt, effectsReconciled,
          abandonAuthorized, abandonReceipt, keyReferenceProof, keyAvailable>>

Init ==
  /\ phase = "Pending"
  /\ owner = "Worker"
  /\ inputPresent = TRUE
  /\ inputTombstone = FALSE
  /\ otherInputPresent = TRUE
  /\ otherInputTombstone = FALSE
  /\ completionReceipt = FALSE
  /\ effectsReconciled = FALSE
  /\ abandonAuthorized = FALSE
  /\ abandonReceipt = FALSE
  /\ keyReferenceProof = FALSE
  /\ keyAvailable = TRUE

HoldRelease ==
  /\ phase = "Pending"
  /\ phase' = "Held"
  /\ UNCHANGED <<owner, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, effectsReconciled,
                   abandonAuthorized, abandonReceipt, keyReferenceProof, keyAvailable>>

LoseWorker ==
  /\ phase = "Held"
  /\ owner = "Worker"
  /\ owner' = "NoOwner"
  /\ UNCHANGED <<phase, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, effectsReconciled,
                   abandonAuthorized, abandonReceipt, keyReferenceProof, keyAvailable>>

SuccessorClaimsReadback ==
  /\ phase = "Held"
  /\ owner = "NoOwner"
  /\ owner' = "Successor"
  /\ UNCHANGED <<phase, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, effectsReconciled,
                   abandonAuthorized, abandonReceipt, keyReferenceProof, keyAvailable>>

ReconcileExternalEffects ==
  /\ phase = "Held"
  /\ owner # "NoOwner"
  /\ effectsReconciled' = TRUE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, abandonAuthorized,
                   abandonReceipt, keyReferenceProof, keyAvailable>>

AuthorizeAbandonment ==
  /\ phase = "Held"
  /\ owner # "NoOwner"
  /\ effectsReconciled
  /\ abandonAuthorized' = TRUE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, effectsReconciled,
                   abandonReceipt, keyReferenceProof, keyAvailable>>

AbandonRelease ==
  /\ phase = "Held"
  /\ owner # "NoOwner"
  /\ (AllowUnsafeAbandon \/ (effectsReconciled /\ abandonAuthorized))
  /\ phase' = "Abandoned"
  /\ abandonReceipt' = TRUE
  /\ UNCHANGED <<owner, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, completionReceipt, effectsReconciled,
                   abandonAuthorized, keyReferenceProof, keyAvailable>>

CompleteRelease ==
  /\ phase = "Pending"
  /\ owner # "NoOwner"
  /\ phase' = "Complete"
  /\ completionReceipt' = TRUE
  /\ effectsReconciled' = TRUE
  /\ UNCHANGED <<owner, inputPresent, inputTombstone, otherInputPresent,
                   otherInputTombstone, abandonAuthorized, abandonReceipt,
                   keyReferenceProof, keyAvailable>>

DeleteInputsAfterDisposition ==
  /\ inputPresent
  /\ ((phase = "Complete" /\ completionReceipt)
      \/ (phase = "Abandoned" /\ abandonReceipt /\ effectsReconciled))
  /\ inputPresent' = FALSE
  /\ inputTombstone' = TRUE
  /\ UNCHANGED <<phase, owner, otherInputPresent, otherInputTombstone,
                   completionReceipt, effectsReconciled, abandonAuthorized,
                   abandonReceipt, keyReferenceProof, keyAvailable>>

UnsafeDeleteHeldInputs ==
  /\ AllowUnsafeDelete
  /\ phase = "Held"
  /\ inputPresent
  /\ inputPresent' = FALSE
  /\ inputTombstone' = TRUE
  /\ UNCHANGED <<phase, owner, otherInputPresent, otherInputTombstone,
                   completionReceipt, effectsReconciled, abandonAuthorized,
                   abandonReceipt, keyReferenceProof, keyAvailable>>

CompleteOtherRecord ==
  /\ otherInputPresent
  /\ otherInputTombstone' = TRUE
  /\ otherInputPresent' = FALSE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone,
                   completionReceipt, effectsReconciled, abandonAuthorized,
                   abandonReceipt, keyReferenceProof, keyAvailable>>

ProveNoKeyReferences ==
  /\ ~inputPresent
  /\ ~otherInputPresent
  /\ inputTombstone
  /\ otherInputTombstone
  /\ keyReferenceProof' = TRUE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone,
                   otherInputPresent, otherInputTombstone, completionReceipt,
                   effectsReconciled, abandonAuthorized, abandonReceipt,
                   keyAvailable>>

RetireUnreferencedKey ==
  /\ keyReferenceProof
  /\ ~inputPresent
  /\ ~otherInputPresent
  /\ keyAvailable
  /\ keyAvailable' = FALSE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone,
                   otherInputPresent, otherInputTombstone, completionReceipt,
                   effectsReconciled, abandonAuthorized, abandonReceipt,
                   keyReferenceProof>>

UnsafeRetireSharedKey ==
  /\ AllowUnsafeKeyRetire
  /\ keyAvailable
  /\ (inputPresent \/ otherInputPresent)
  /\ keyAvailable' = FALSE
  /\ UNCHANGED <<phase, owner, inputPresent, inputTombstone,
                   otherInputPresent, otherInputTombstone, completionReceipt,
                   effectsReconciled, abandonAuthorized, abandonReceipt,
                   keyReferenceProof>>

Done ==
  /\ phase \in {"Complete", "Abandoned"}
  /\ ~inputPresent
  /\ inputTombstone
  /\ ~otherInputPresent
  /\ otherInputTombstone
  /\ ~keyAvailable
  /\ UNCHANGED vars

Next == HoldRelease \/ LoseWorker \/ SuccessorClaimsReadback \/
        ReconcileExternalEffects \/ AuthorizeAbandonment \/ AbandonRelease \/
        CompleteRelease \/ DeleteInputsAfterDisposition \/
        UnsafeDeleteHeldInputs \/ CompleteOtherRecord \/
        ProveNoKeyReferences \/ RetireUnreferencedKey \/
        UnsafeRetireSharedKey \/ Done

AbandonRequiresReadbackAndAuthority == phase = "Abandoned" =>
  effectsReconciled /\ abandonAuthorized /\ abandonReceipt

InputDeletionRequiresTerminalDisposition == ~inputPresent =>
  inputTombstone /\ ((phase = "Complete" /\ completionReceipt) \/
                      (phase = "Abandoned" /\ abandonReceipt /\ effectsReconciled))

OtherInputDeletionIsTombstoned == ~otherInputPresent => otherInputTombstone

NoKeyRetirementWhileInputsRemain == ~keyAvailable =>
  ~inputPresent /\ ~otherInputPresent /\ keyReferenceProof

Spec == Init /\ [][Next]_vars
=============================================================================
