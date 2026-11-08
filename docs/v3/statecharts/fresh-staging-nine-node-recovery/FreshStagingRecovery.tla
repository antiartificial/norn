-------------------------- MODULE FreshStagingRecovery --------------------------
EXTENDS Naturals, TLC

\* Proposed 3-control + 2-ingress + 4-app topology only. The checked-in
\* staging/nyc3 source declares five compute nodes and no app pools.
MaxNodes == 9

VARIABLES phase, inputsValid, intentBound, nonceUsed, providerNodes,
          confirmedNodes, uncertain, identityExact, freshReadback, providerMismatch,
          inventoryReady, pkiReady, secretsMaterialized, configuredNodes,
          terminalReceipt

vars == <<phase, inputsValid, intentBound, nonceUsed, providerNodes,
          confirmedNodes, uncertain, identityExact, freshReadback, providerMismatch,
          inventoryReady, pkiReady, secretsMaterialized, configuredNodes,
          terminalReceipt>>

Init ==
    /\ phase = "inputs"
    /\ inputsValid = FALSE
    /\ intentBound = FALSE
    /\ nonceUsed = FALSE
    /\ providerNodes = 0
    /\ confirmedNodes = 0
    /\ uncertain = FALSE
    /\ identityExact = TRUE
    /\ freshReadback = FALSE
    /\ providerMismatch = FALSE
    /\ inventoryReady = FALSE
    /\ pkiReady = FALSE
    /\ secretsMaterialized = FALSE
    /\ configuredNodes = 0
    /\ terminalReceipt = FALSE

ValidateAllInputs ==
    /\ phase = "inputs"
    /\ inputsValid' = TRUE
    /\ phase' = "secrets"
    /\ UNCHANGED <<intentBound, nonceUsed, providerNodes, confirmedNodes,
                   uncertain, identityExact, freshReadback, providerMismatch,
                   inventoryReady, pkiReady, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

MaterializeBoundSecrets ==
    /\ phase = "secrets"
    /\ inputsValid
    /\ secretsMaterialized' = TRUE
    /\ phase' = "ready"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, pkiReady, configuredNodes,
                   terminalReceipt>>

RejectInvalidOrStaleInput ==
    /\ phase = "inputs"
    /\ phase' = "held"
    /\ freshReadback' = FALSE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, providerMismatch,
                   inventoryReady, pkiReady, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

AdmitOneUseIntent ==
    /\ phase = "ready"
    /\ inputsValid /\ secretsMaterialized
    /\ ~nonceUsed
    /\ intentBound' = TRUE
    /\ nonceUsed' = TRUE
    /\ phase' = "applying"
    /\ UNCHANGED <<inputsValid, providerNodes, confirmedNodes, uncertain,
                   identityExact, freshReadback, providerMismatch, inventoryReady,
                   pkiReady, secretsMaterialized, configuredNodes, terminalReceipt>>

CreateOneNode ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ ~uncertain
    /\ providerNodes = confirmedNodes
    /\ providerNodes < MaxNodes
    /\ providerNodes' = providerNodes + 1
    /\ confirmedNodes' = confirmedNodes + 1
    /\ identityExact' = identityExact
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, uncertain,
                   freshReadback, providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

ProviderEffectThenLostResponse ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ identityExact
    /\ ~uncertain
    /\ providerNodes = confirmedNodes
    /\ providerNodes < MaxNodes
    /\ providerNodes' = providerNodes + 1
    /\ uncertain' = TRUE
    /\ phase' = "held"
    /\ identityExact' = FALSE
    /\ freshReadback' = FALSE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, confirmedNodes,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

ControllerCrash ==
    /\ phase \in {"applying", "checking", "recovering", "created", "inventory",
                   "configuring"}
    /\ phase' = "held"
    /\ freshReadback' = FALSE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, providerMismatch,
                   inventoryReady, pkiReady, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

BeginStateCheck ==
    /\ phase \in {"applying", "recovering"}
    /\ providerNodes = MaxNodes
    /\ providerNodes = confirmedNodes
    /\ phase' = "checking"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

ReadBackProviderState ==
    /\ phase = "held"
    /\ confirmedNodes' = providerNodes
    /\ uncertain' = FALSE
    /\ identityExact' = ~providerMismatch
    /\ freshReadback' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

ConfirmSuccessfulApplyState ==
    /\ phase = "checking"
    /\ providerNodes = MaxNodes
    /\ providerNodes = confirmedNodes
    /\ ~providerMismatch
    /\ identityExact' = TRUE
    /\ freshReadback' = TRUE
    /\ phase' = "created"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, providerMismatch, inventoryReady,
                   pkiReady, secretsMaterialized, configuredNodes, terminalReceipt>>

HoldMismatchedState ==
    /\ phase = "checking"
    /\ providerMismatch
    /\ phase' = "held"
    /\ identityExact' = FALSE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, providerMismatch, inventoryReady,
                   pkiReady, freshReadback, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

DetectUnclassifiedResource ==
    /\ phase \in {"applying", "recovering", "checking"}
    /\ providerMismatch' = TRUE
    /\ identityExact' = FALSE
    /\ freshReadback' = TRUE
    /\ phase' = "held"
    /\ uncertain' = FALSE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

RecoverSameIntent ==
    /\ phase = "held"
    /\ intentBound /\ nonceUsed
    /\ ~uncertain
    /\ freshReadback
    /\ identityExact
    /\ confirmedNodes = providerNodes
    /\ phase' = IF providerNodes = MaxNodes THEN
                    IF inventoryReady THEN "configuring" ELSE "created"
                 ELSE "recovering"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes, terminalReceipt>>

GenerateInventory ==
    /\ phase = "created"
    /\ identityExact
    /\ providerNodes = MaxNodes
    /\ confirmedNodes = MaxNodes
    /\ inventoryReady' = TRUE
    /\ phase' = "inventory"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, pkiReady, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

PrepareClusterPKI ==
    /\ phase = "inventory"
    /\ inventoryReady
    /\ pkiReady' = TRUE
    /\ phase' = "configuring"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, secretsMaterialized,
                   configuredNodes, terminalReceipt>>

ConfigureOneNode ==
    /\ phase = "configuring"
    /\ inventoryReady /\ pkiReady /\ secretsMaterialized
    /\ configuredNodes < MaxNodes
    /\ configuredNodes' = configuredNodes + 1
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, terminalReceipt>>

CompleteWithReceipt ==
    /\ phase = "configuring"
    /\ configuredNodes = MaxNodes
    /\ providerNodes = MaxNodes
    /\ identityExact
    /\ inventoryReady /\ pkiReady /\ secretsMaterialized
    /\ terminalReceipt' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerNodes,
                   confirmedNodes, uncertain, identityExact, freshReadback,
                   providerMismatch, inventoryReady, pkiReady,
                   secretsMaterialized, configuredNodes>>

Next ==
    ValidateAllInputs \/ MaterializeBoundSecrets
    \/ RejectInvalidOrStaleInput \/ AdmitOneUseIntent
    \/ CreateOneNode \/ ProviderEffectThenLostResponse \/ ControllerCrash
    \/ BeginStateCheck \/ ReadBackProviderState \/ ConfirmSuccessfulApplyState
    \/ HoldMismatchedState \/ DetectUnclassifiedResource
    \/ RecoverSameIntent \/ GenerateInventory
    \/ PrepareClusterPKI \/ ConfigureOneNode \/ CompleteWithReceipt

Done == phase = "complete" /\ UNCHANGED vars
SafeHoldStutter ==
    /\ phase = "held"
    /\ (~intentBound \/ providerMismatch)
    /\ UNCHANGED vars
Spec == Init /\ [][Next \/ Done \/ SafeHoldStutter]_vars

TypeOK ==
    /\ phase \in {"inputs", "secrets", "ready", "applying", "checking", "held", "recovering",
                   "created", "inventory", "configuring", "complete"}
    /\ providerNodes \in 0..MaxNodes
    /\ confirmedNodes \in 0..MaxNodes
    /\ configuredNodes \in 0..MaxNodes
NoCreateBeforeIntent == providerNodes > 0 => (intentBound /\ nonceUsed)
UnknownEffectIsHeld == uncertain => (phase = "held" /\ ~freshReadback)
NoRecoveryWriteWithoutExactReadback ==
    phase = "recovering" => (freshReadback /\ identityExact /\ ~uncertain /\ confirmedNodes = providerNodes)
NoDuplicateProviderCreate == confirmedNodes <= providerNodes
NoConfigurationBeforeFullInventory ==
    configuredNodes > 0 => (inventoryReady /\ pkiReady /\ secretsMaterialized)
NoInventoryFromPartialState == inventoryReady => (providerNodes = MaxNodes /\ confirmedNodes = MaxNodes /\ identityExact)
NoTerminalReceiptFromPartialFleet ==
    terminalReceipt => (phase = "complete" /\ providerNodes = MaxNodes /\
                        configuredNodes = MaxNodes /\ identityExact /\
                        inventoryReady /\ pkiReady /\ secretsMaterialized)
UnknownProviderStateHolds == providerMismatch /\ phase = "held" => ~identityExact

=============================================================================
