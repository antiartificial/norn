-------------------- MODULE RecoveryDescendant --------------------
EXTENDS Naturals, FiniteSets, TLC

CONSTANTS Recovery, Descendant, Unrelated, GateMode
ASSUME Recovery # Descendant
ASSUME Recovery # Unrelated
ASSUME Descendant # Unrelated
ASSUME GateMode \in {"signed-descendants", "recovery-only"}

Phases == {"recovery-ready", "descendant-running", "unrelated-running",
           "upgrade-authorized", "held"}
Releases == {Recovery, Descendant, Unrelated}

VARIABLES phase, activeRelease, apiRelease, hostAgentRelease,
          recoveryEvidenceValid, activeReleaseSigned, activeReleaseDescends,
          gatePassed

vars == <<phase, activeRelease, apiRelease, hostAgentRelease,
          recoveryEvidenceValid, activeReleaseSigned, activeReleaseDescends,
          gatePassed>>

Init ==
  /\ phase = "recovery-ready"
  /\ activeRelease = Recovery
  /\ apiRelease = Recovery
  /\ hostAgentRelease = Recovery
  /\ recoveryEvidenceValid = TRUE
  /\ activeReleaseSigned = TRUE
  /\ activeReleaseDescends = TRUE
  /\ gatePassed = FALSE

GateAccepts ==
  /\ recoveryEvidenceValid
  /\ activeReleaseSigned
  /\ activeReleaseDescends
  /\ apiRelease = activeRelease
  /\ hostAgentRelease = activeRelease
  /\ activeRelease \in {Recovery, Descendant}
  /\ (GateMode = "signed-descendants" \/ activeRelease = Recovery)

CheckUpgradeGate ==
  /\ phase \in {"recovery-ready", "descendant-running", "unrelated-running"}
  /\ phase' = IF GateAccepts THEN "upgrade-authorized" ELSE "held"
  /\ gatePassed' = GateAccepts
  /\ UNCHANGED <<activeRelease, apiRelease, hostAgentRelease,
                  recoveryEvidenceValid, activeReleaseSigned,
                  activeReleaseDescends>>

PromoteFirstSignedDescendant ==
  /\ phase = "upgrade-authorized"
  /\ activeRelease = Recovery
  /\ gatePassed
  /\ activeRelease' = Descendant
  /\ apiRelease' = Descendant
  /\ hostAgentRelease' = Descendant
  /\ activeReleaseSigned' = TRUE
  /\ activeReleaseDescends' = TRUE
  /\ phase' = "descendant-running"
  /\ gatePassed' = FALSE
  /\ UNCHANGED recoveryEvidenceValid

RunUnrelatedSignedRelease ==
  /\ phase = "recovery-ready"
  /\ activeRelease' = Unrelated
  /\ apiRelease' = Unrelated
  /\ hostAgentRelease' = Unrelated
  /\ activeReleaseSigned' = TRUE
  /\ activeReleaseDescends' = FALSE
  /\ phase' = "unrelated-running"
  /\ gatePassed' = FALSE
  /\ UNCHANGED recoveryEvidenceValid

HeldWait == /\ phase = "held" /\ UNCHANGED vars
AuthorizedWait ==
  /\ phase = "upgrade-authorized"
  /\ activeRelease = Descendant
  /\ UNCHANGED vars

Next == CheckUpgradeGate \/ PromoteFirstSignedDescendant \/ RunUnrelatedSignedRelease \/
        HeldWait \/ AuthorizedWait
Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in Phases
  /\ activeRelease \in Releases
  /\ apiRelease \in Releases
  /\ hostAgentRelease \in Releases
  /\ recoveryEvidenceValid \in BOOLEAN
  /\ activeReleaseSigned \in BOOLEAN
  /\ activeReleaseDescends \in BOOLEAN
  /\ gatePassed \in BOOLEAN

GateBindsRuntimeToSignedDescendant ==
  gatePassed =>
    (recoveryEvidenceValid
     /\ activeReleaseSigned
     /\ activeReleaseDescends
     /\ activeRelease \in {Recovery, Descendant}
     /\ apiRelease = activeRelease
     /\ hostAgentRelease = activeRelease)

VerifiedLaterReleaseRemainsUpgradeable ==
  (activeRelease = Descendant
   /\ recoveryEvidenceValid
   /\ activeReleaseSigned
   /\ activeReleaseDescends
   /\ apiRelease = activeRelease
   /\ hostAgentRelease = activeRelease)
  => phase # "held"

UnrelatedReleaseCannotPassGate ==
  activeRelease = Unrelated => ~gatePassed

RecoveryEvidenceIsNeverDiscarded == recoveryEvidenceValid

=============================================================================
