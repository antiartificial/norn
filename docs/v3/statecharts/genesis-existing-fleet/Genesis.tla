---------------- MODULE Genesis ----------------
EXTENDS Naturals, FiniteSets

CONSTANTS Controls, Serving, MaxFenceEpoch

ASSUME Serving \in Controls
ASSUME MaxFenceEpoch \in Nat
ASSUME MaxFenceEpoch >= 1

VARIABLES phase, inputStatus, installDone, genesisIntentVerified,
          unitsActive, journal, attempt, staged, promoted, fenceEpoch,
          recoveryProof, providerResources, heldReason

vars == <<phase, inputStatus, installDone, genesisIntentVerified,
         unitsActive, journal, attempt, staged, promoted, fenceEpoch,
         recoveryProof, providerResources, heldReason>>

Init ==
    /\ phase = "start"
    /\ inputStatus = "unknown"
    /\ installDone = FALSE
    /\ genesisIntentVerified = FALSE
    /\ unitsActive = FALSE
    /\ journal = "empty"
    /\ attempt = "none"
    /\ staged = {}
    /\ promoted = {}
    /\ fenceEpoch = 0
    /\ recoveryProof = FALSE
    /\ providerResources = {}
    /\ heldReason = "none"

ReviewValidInputs ==
    /\ phase = "start"
    /\ inputStatus = "unknown"
    /\ inputStatus' = "valid"
    /\ phase' = "inputs-checked"
    /\ UNCHANGED <<installDone, genesisIntentVerified, unitsActive, journal,
                   attempt, staged, promoted, fenceEpoch, recoveryProof,
                   providerResources, heldReason>>

RejectInvalidInputs ==
    /\ phase = "start"
    /\ inputStatus = "unknown"
    /\ inputStatus' = "invalid"
    /\ phase' = "held"
    /\ heldReason' = "input-validation"
    /\ UNCHANGED <<installDone, genesisIntentVerified, unitsActive, journal,
                   attempt, staged, promoted, fenceEpoch, recoveryProof,
                   providerResources>>

InstallDormantController ==
    /\ phase = "inputs-checked"
    /\ inputStatus = "valid"
    /\ installDone = FALSE
    /\ installDone' = TRUE
    /\ unitsActive' = FALSE
    /\ phase' = "dormant-installed"
    /\ UNCHANGED <<inputStatus, genesisIntentVerified, journal, attempt,
                   staged, promoted, fenceEpoch, recoveryProof,
                   providerResources, heldReason>>

VerifyGenesisIntent ==
    /\ phase = "dormant-installed"
    /\ installDone
    /\ inputStatus = "valid"
    /\ genesisIntentVerified = FALSE
    /\ genesisIntentVerified' = TRUE
    /\ journal' = "intent-bound"
    /\ phase' = "genesis-ready"
    /\ UNCHANGED <<inputStatus, installDone, unitsActive, attempt, staged,
                   promoted, fenceEpoch, recoveryProof, providerResources,
                   heldReason>>

EnableGenesis ==
    /\ phase = "genesis-ready"
    /\ genesisIntentVerified
    /\ unitsActive = FALSE
    /\ unitsActive' = TRUE
    /\ phase' = "enabled"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified, journal,
                   attempt, staged, promoted, fenceEpoch, recoveryProof,
                   providerResources, heldReason>>

StartGenesis ==
    /\ phase = "enabled"
    /\ unitsActive
    /\ journal = "intent-bound"
    /\ attempt = "none"
    /\ attempt' = "running"
    /\ fenceEpoch' = 1
    /\ phase' = "running"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, staged, promoted, recoveryProof,
                   providerResources, heldReason>>

StageControl(c) ==
    /\ phase = "running"
    /\ attempt = "running"
    /\ c \in Controls \ staged
    /\ staged' = staged \cup {c}
    /\ UNCHANGED <<phase, inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, attempt, promoted, fenceEpoch,
                   recoveryProof, providerResources, heldReason>>

PromotePassive(c) ==
    /\ phase = "running"
    /\ attempt = "running"
    /\ staged = Controls
    /\ c \in Controls \ {Serving}
    /\ c \notin promoted
    /\ promoted' = promoted \cup {c}
    /\ UNCHANGED <<phase, inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, attempt, staged, fenceEpoch,
                   recoveryProof, providerResources, heldReason>>

PromoteServing ==
    /\ phase = "running"
    /\ attempt = "running"
    /\ staged = Controls
    /\ Controls \ {Serving} \subseteq promoted
    /\ Serving \notin promoted
    /\ promoted' = promoted \cup {Serving}
    /\ UNCHANGED <<phase, inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, attempt, staged, fenceEpoch,
                   recoveryProof, providerResources, heldReason>>

CompleteGenesis ==
    /\ phase = "running"
    /\ attempt = "running"
    /\ promoted = Controls
    /\ attempt' = "succeeded"
    /\ phase' = "complete"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, staged, promoted, fenceEpoch,
                   recoveryProof, providerResources, heldReason>>

ControllerCrash ==
    /\ phase = "running"
    /\ attempt = "running"
    /\ attempt' = "interrupted"
    /\ phase' = "held"
    /\ heldReason' = "interrupted-effect-needs-recovery"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, staged, promoted, fenceEpoch,
                   recoveryProof, providerResources>>

AuthorizeRecovery ==
    /\ phase = "held"
    /\ attempt = "interrupted"
    /\ fenceEpoch < MaxFenceEpoch
    /\ recoveryProof = FALSE
    /\ recoveryProof' = TRUE
    /\ heldReason' = "recovery-proof-recorded"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, attempt, staged, promoted,
                   fenceEpoch, providerResources, phase>>

ResumeSameIntent ==
    /\ phase = "held"
    /\ attempt = "interrupted"
    /\ recoveryProof
    /\ attempt' = "running"
    /\ fenceEpoch' = fenceEpoch + 1
    /\ phase' = "running"
    /\ heldReason' = "none"
    /\ recoveryProof' = FALSE
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, staged, promoted, providerResources>>

FreshFleetRequestIsBlocked ==
    /\ phase = "start"
    /\ inputStatus = "unknown"
    /\ phase' = "fresh-fleet-blocked"
    /\ heldReason' = "no-provider-create-lane-in-this-controller"
    /\ UNCHANGED <<inputStatus, installDone, genesisIntentVerified,
                   unitsActive, journal, attempt, staged, promoted, fenceEpoch,
                   recoveryProof, providerResources>>

Done ==
    /\ (phase \in {"complete", "fresh-fleet-blocked"}
        \/ (phase = "held" /\ attempt # "interrupted")
        \/ (phase = "held" /\ attempt = "interrupted" /\ fenceEpoch >= MaxFenceEpoch))
    /\ UNCHANGED vars

Next ==
    \/ ReviewValidInputs
    \/ RejectInvalidInputs
    \/ InstallDormantController
    \/ VerifyGenesisIntent
    \/ EnableGenesis
    \/ StartGenesis
    \/ \E c \in Controls: StageControl(c)
    \/ \E c \in Controls: PromotePassive(c)
    \/ PromoteServing
    \/ CompleteGenesis
    \/ ControllerCrash
    \/ AuthorizeRecovery
    \/ ResumeSameIntent
    \/ FreshFleetRequestIsBlocked
    \/ Done

NoEffectsBeforeIntent == ((staged = {}) /\ (promoted = {})) \/ genesisIntentVerified
DormantInstallStaysOff == phase = "dormant-installed" => ~unitsActive
GenesisRequiresInstalledController == genesisIntentVerified => installDone
PromotionRequiresCompleteStaging == promoted # {} => staged = Controls
ServingPromotesLast == Serving \in promoted => Controls \ {Serving} \subseteq promoted
SuccessMeansAllControlsPromoted == attempt = "succeeded" => promoted = Controls
RecoveryProofIsOneUse == recoveryProof => phase = "held" /\ attempt = "interrupted"
FreshProvisioningHasNoProviderEffects == providerResources = {}

=================================================
