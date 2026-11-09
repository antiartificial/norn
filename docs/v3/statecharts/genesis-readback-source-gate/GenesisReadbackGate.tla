---------------- MODULE GenesisReadbackGate ----------------
EXTENDS Naturals, FiniteSets

CONSTANTS OldSource, CurrentSource
ASSUME OldSource # CurrentSource

Bindings == {"valid", "wrong-state", "wrong-host-keys", "bad-signature"}
Phases == {"start", "intent-staged", "readback-held", "verifier-installed", "readback-complete", "intent-rejected"}
IntentStates == {"none", "fresh", "spent"}
ReadbackStates == {"not-run", "source-mismatch", "input-rejected", "verified"}

VARIABLES phase, installedSource, intentState,
          binding, readback, genesisUnits, providerMutation

vars == <<phase, installedSource, intentState, binding, readback, genesisUnits, providerMutation>>

Init ==
    /\ phase = "start"
    /\ installedSource = OldSource
    /\ intentState = "none"
    /\ binding = "valid"
    /\ readback = "not-run"
    /\ genesisUnits = "off"
    /\ providerMutation = FALSE

StageReadbackIntent(chosenBinding) ==
    /\ phase \in {"start", "readback-held", "verifier-installed", "intent-rejected"}
    /\ intentState # "fresh"
    /\ chosenBinding \in Bindings
    /\ phase' = "intent-staged"
    /\ intentState' = "fresh"
    /\ binding' = chosenBinding
    /\ readback' = "not-run"
    /\ UNCHANGED <<installedSource, genesisUnits, providerMutation>>

RejectInvalidReadbackInputs ==
    /\ phase = "intent-staged"
    /\ intentState = "fresh"
    /\ binding # "valid"
    /\ phase' = "intent-rejected"
    /\ intentState' = "spent"
    /\ readback' = "input-rejected"
    /\ UNCHANGED <<installedSource, binding, genesisUnits, providerMutation>>

ReadbackWithStaleSource ==
    /\ phase = "intent-staged"
    /\ intentState = "fresh"
    /\ binding = "valid"
    /\ installedSource = OldSource
    /\ genesisUnits = "off"
    /\ phase' = "readback-held"
    /\ intentState' = "spent"
    /\ readback' = "source-mismatch"
    /\ UNCHANGED <<installedSource, binding, genesisUnits, providerMutation>>

InstallReviewedVerifier ==
    /\ phase = "readback-held"
    /\ readback = "source-mismatch"
    /\ installedSource = OldSource
    /\ genesisUnits = "off"
    /\ installedSource' = CurrentSource
    /\ phase' = "verifier-installed"
    /\ UNCHANGED <<intentState, binding, readback, genesisUnits, providerMutation>>

ReadbackWithCurrentSource ==
    /\ phase = "intent-staged"
    /\ intentState = "fresh"
    /\ binding = "valid"
    /\ installedSource = CurrentSource
    /\ genesisUnits = "off"
    /\ phase' = "readback-complete"
    /\ intentState' = "spent"
    /\ readback' = "verified"
    /\ UNCHANGED <<installedSource, binding, genesisUnits, providerMutation>>

Done ==
    /\ phase \in {"readback-complete", "intent-rejected"}
    /\ UNCHANGED vars

Next ==
    \/ \E b \in Bindings: StageReadbackIntent(b)
    \/ RejectInvalidReadbackInputs
    \/ ReadbackWithStaleSource
    \/ InstallReviewedVerifier
    \/ ReadbackWithCurrentSource
    \/ Done

TypeOK ==
    /\ phase \in Phases
    /\ installedSource \in {OldSource, CurrentSource}
    /\ intentState \in IntentStates
    /\ binding \in Bindings
    /\ readback \in ReadbackStates
    /\ genesisUnits = "off"
    /\ providerMutation = FALSE

VerifiedReadbackUsesCurrentSource ==
    readback = "verified" => installedSource = CurrentSource

ReadbackConsumesOneUseIntent ==
    readback \in {"source-mismatch", "input-rejected", "verified"} => intentState = "spent"

NoFleetProviderMutation == providerMutation = FALSE
GenesisRemainsDormant == genesisUnits = "off"

Spec == Init /\ [][Next]_vars

====
