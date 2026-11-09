------------------------ MODULE FreshTwoAppPools ------------------------
EXTENDS Naturals, FiniteSets

VARIABLES phase, inputsValid, intentBound, nonceUsed,
          providerA, providerB, attemptsA, attemptsB,
          uncertainA, uncertainB, observedA, observedB, providerMismatch,
          inventoryReady, pkiReady, secretsReady, configuredA, configuredB,
          terminalReceipt, workloads

vars == <<phase, inputsValid, intentBound, nonceUsed,
          providerA, providerB, attemptsA, attemptsB,
          uncertainA, uncertainB, observedA, observedB, providerMismatch,
          inventoryReady, pkiReady, secretsReady, configuredA, configuredB,
          terminalReceipt, workloads>>

\* Pool A is the general application pool; Pool B is the stateful pool.

Init ==
    /\ phase = "inputs"
    /\ inputsValid = FALSE
    /\ intentBound = FALSE
    /\ nonceUsed = FALSE
    /\ providerA = FALSE
    /\ providerB = FALSE
    /\ attemptsA = 0
    /\ attemptsB = 0
    /\ uncertainA = FALSE
    /\ uncertainB = FALSE
    /\ observedA = FALSE
    /\ observedB = FALSE
    /\ providerMismatch = FALSE
    /\ inventoryReady = FALSE
    /\ pkiReady = FALSE
    /\ secretsReady = FALSE
    /\ configuredA = FALSE
    /\ configuredB = FALSE
    /\ terminalReceipt = FALSE
    /\ workloads = [generalPlaced |-> FALSE, statefulPlaced |-> FALSE,
                    generalReady |-> FALSE, statefulReady |-> FALSE,
                    statefulRecoveryVerified |-> FALSE]

ValidateInputs ==
    /\ phase = "inputs"
    /\ phase' = "ready"
    /\ inputsValid' = TRUE
    /\ UNCHANGED <<intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt, workloads>>

RejectInputs ==
    /\ phase = "inputs"
    /\ phase' = "held"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt, workloads>>

BindIntent ==
    /\ phase = "ready"
    /\ inputsValid
    /\ ~intentBound
    /\ phase' = "applying"
    /\ intentBound' = TRUE
    /\ nonceUsed' = TRUE
    /\ UNCHANGED <<inputsValid, providerA, providerB, attemptsA, attemptsB,
                    uncertainA, uncertainB, observedA, observedB,
                    providerMismatch, inventoryReady, pkiReady, secretsReady,
                    configuredA, configuredB, terminalReceipt, workloads>>

CreateA ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ ~providerA
    /\ ~uncertainA
    /\ providerA' = TRUE
    /\ observedA' = TRUE
    /\ attemptsA' = attemptsA + 1
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed,
                    providerB, attemptsB, uncertainA, uncertainB,
                    observedB, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

LoseCreateAResponse ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ ~providerA
    /\ ~uncertainA
    /\ providerA' = TRUE
    /\ observedA' = FALSE
    /\ attemptsA' = attemptsA + 1
    /\ uncertainA' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerB, attemptsB,
                    uncertainB, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt, workloads>>

CreateB ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ ~providerB
    /\ ~uncertainB
    /\ providerB' = TRUE
    /\ observedB' = TRUE
    /\ attemptsB' = attemptsB + 1
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed,
                    providerA, attemptsA, uncertainA, uncertainB,
                    observedA, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

LoseCreateBResponse ==
    /\ phase \in {"applying", "recovering"}
    /\ intentBound /\ nonceUsed
    /\ ~providerB
    /\ ~uncertainB
    /\ providerB' = TRUE
    /\ observedB' = FALSE
    /\ attemptsB' = attemptsB + 1
    /\ uncertainB' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, attemptsA,
                    uncertainA, observedA, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt, workloads>>

ReadbackA ==
    /\ phase = "held"
    /\ uncertainA /\ providerA
    /\ observedA' = TRUE
    /\ uncertainA' = FALSE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed,
                    providerA, providerB, attemptsA, attemptsB, uncertainB,
                    observedB, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

ReadbackB ==
    /\ phase = "held"
    /\ uncertainB /\ providerB
    /\ observedB' = TRUE
    /\ uncertainB' = FALSE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed,
                    providerA, providerB, attemptsA, attemptsB, uncertainA,
                    observedA, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

DetectProviderMismatch ==
    /\ phase \in {"applying", "recovering", "held"}
    /\ intentBound
    /\ (providerA \/ providerB)
    /\ providerMismatch' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB, observedA,
                    observedB, inventoryReady, pkiReady, secretsReady,
                    configuredA, configuredB, terminalReceipt, workloads>>

ResumeSameIntent ==
    /\ phase = "held"
    /\ intentBound /\ nonceUsed
    /\ ~uncertainA /\ ~uncertainB
    /\ ~providerMismatch
    /\ phase' = "recovering"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB, observedA,
                    observedB, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

BuildInventory ==
    /\ phase \in {"applying", "recovering"}
    /\ providerA /\ providerB /\ observedA /\ observedB
    /\ ~uncertainA /\ ~uncertainB /\ ~providerMismatch
    /\ phase' = "inventory"
    /\ inventoryReady' = TRUE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB, observedA,
                    observedB, providerMismatch, pkiReady, secretsReady,
                    configuredA, configuredB, terminalReceipt, workloads>>

GeneratePKI ==
    /\ phase = "inventory"
    /\ inventoryReady
    /\ pkiReady' = TRUE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

MaterializeSecrets ==
    /\ phase = "inventory"
    /\ inventoryReady
    /\ secretsReady' = TRUE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, configuredA, configuredB, terminalReceipt, workloads>>

ConfigureFleet ==
    /\ phase = "inventory"
    /\ inventoryReady /\ pkiReady /\ secretsReady
    /\ phase' = "configuring"
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB, observedA,
                    observedB, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, terminalReceipt, workloads>>

ConfigureA ==
    /\ phase = "configuring"
    /\ inventoryReady /\ pkiReady /\ secretsReady
    /\ ~configuredA
    /\ configuredA' = TRUE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredB, terminalReceipt, workloads>>

ConfigureB ==
    /\ phase = "configuring"
    /\ inventoryReady /\ pkiReady /\ secretsReady
    /\ ~configuredB
    /\ configuredB' = TRUE
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, terminalReceipt, workloads>>

PlaceGeneralWorkload ==
    /\ phase = "configuring"
    /\ configuredA
    /\ ~workloads.generalPlaced
    /\ workloads' = [workloads EXCEPT !.generalPlaced = TRUE]
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt>>

PlaceStatefulWorkload ==
    /\ phase = "configuring"
    /\ configuredB
    /\ ~workloads.statefulPlaced
    /\ workloads' = [workloads EXCEPT !.statefulPlaced = TRUE]
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt>>

VerifyGeneralReadiness ==
    /\ phase = "configuring"
    /\ workloads.generalPlaced
    /\ ~workloads.generalReady
    /\ workloads' = [workloads EXCEPT !.generalReady = TRUE]
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt>>

VerifyStatefulRecovery ==
    /\ phase = "configuring"
    /\ workloads.statefulPlaced
    /\ ~workloads.statefulReady
    /\ workloads' = [workloads EXCEPT !.statefulReady = TRUE,
                                      !.statefulRecoveryVerified = TRUE]
    /\ UNCHANGED <<phase, inputsValid, intentBound, nonceUsed, providerA,
                    providerB, attemptsA, attemptsB, uncertainA, uncertainB,
                    observedA, observedB, providerMismatch, inventoryReady,
                    pkiReady, secretsReady, configuredA, configuredB,
                    terminalReceipt>>

CommitReceipt ==
    /\ phase = "configuring"
    /\ configuredA /\ configuredB
    /\ workloads.generalReady /\ workloads.statefulReady
    /\ workloads.statefulRecoveryVerified
    /\ providerA /\ providerB /\ observedA /\ observedB
    /\ inventoryReady /\ pkiReady /\ secretsReady
    /\ phase' = "complete"
    /\ terminalReceipt' = TRUE
    /\ UNCHANGED <<inputsValid, intentBound, nonceUsed, providerA, providerB,
                    attemptsA, attemptsB, uncertainA, uncertainB, observedA,
                    observedB, providerMismatch, inventoryReady, pkiReady,
                    secretsReady, configuredA, configuredB, workloads>>

Done == phase \in {"complete", "held"} /\ UNCHANGED vars

Next == ValidateInputs \/ RejectInputs \/ BindIntent \/ CreateA \/ LoseCreateAResponse \/
        CreateB \/ LoseCreateBResponse \/ ReadbackA \/ ReadbackB \/
        DetectProviderMismatch \/ ResumeSameIntent \/ BuildInventory \/
        GeneratePKI \/ MaterializeSecrets \/ ConfigureFleet \/ ConfigureA \/
        ConfigureB \/ PlaceGeneralWorkload \/ PlaceStatefulWorkload \/
        VerifyGeneralReadiness \/ VerifyStatefulRecovery \/ CommitReceipt \/ Done

TypeOK ==
    /\ phase \in {"inputs", "ready", "applying", "held", "recovering", "inventory", "configuring", "complete"}
    /\ inputsValid \in BOOLEAN
    /\ intentBound \in BOOLEAN
    /\ nonceUsed \in BOOLEAN
    /\ providerA \in BOOLEAN /\ providerB \in BOOLEAN
    /\ attemptsA \in 0..1 /\ attemptsB \in 0..1
    /\ uncertainA \in BOOLEAN /\ uncertainB \in BOOLEAN
    /\ observedA \in BOOLEAN /\ observedB \in BOOLEAN
    /\ providerMismatch \in BOOLEAN
    /\ inventoryReady \in BOOLEAN /\ pkiReady \in BOOLEAN
    /\ secretsReady \in BOOLEAN /\ configuredA \in BOOLEAN
    /\ configuredB \in BOOLEAN /\ terminalReceipt \in BOOLEAN
    /\ workloads \in [generalPlaced: BOOLEAN, statefulPlaced: BOOLEAN,
                         generalReady: BOOLEAN, statefulReady: BOOLEAN,
                         statefulRecoveryVerified: BOOLEAN]

NoCreateBeforeIntent == (providerA \/ providerB) => (intentBound /\ nonceUsed)
UnknownEffectIsHeld == (uncertainA \/ uncertainB) => phase = "held"
NoRecoveryWriteWithoutReadback == phase = "recovering" => (~uncertainA /\ ~uncertainB /\ ~providerMismatch)
NoDuplicateProviderCreate == (attemptsA <= 1 /\ attemptsB <= 1)
NoInventoryFromPartialPools == inventoryReady => (observedA /\ observedB /\ ~providerMismatch)
NoConfigurationBeforeFullInventory == (configuredA \/ configuredB) => (inventoryReady /\ pkiReady /\ secretsReady)
NoTerminalReceiptFromPartialFleet == terminalReceipt => (phase = "complete" /\ providerA /\ providerB /\ observedA /\ observedB /\ configuredA /\ configuredB /\ workloads.generalReady /\ workloads.statefulReady /\ workloads.statefulRecoveryVerified)
NoSuccessWithMismatchedProvider == terminalReceipt => ~providerMismatch
GeneralWorkloadOnGeneralPool == workloads.generalPlaced => configuredA
StatefulWorkloadOnStatefulPool == workloads.statefulPlaced => configuredB
GeneralReadinessRequiresPlacement == workloads.generalReady => workloads.generalPlaced
StatefulReadinessRequiresPlacement == workloads.statefulReady => workloads.statefulPlaced
StatefulSuccessRequiresRecovery == terminalReceipt => workloads.statefulRecoveryVerified

Spec == Init /\ [][Next]_vars

=============================================================================
