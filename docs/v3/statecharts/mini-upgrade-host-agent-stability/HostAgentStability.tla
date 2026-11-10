-------------------- MODULE HostAgentStability --------------------
EXTENDS Naturals, TLC

CONSTANTS Old, Target
ASSUME Old # Target

Phases == {"checking", "admitted", "held", "promoted"}

VARIABLES phase, source, schema, agentPID, childPID, previousPID, previousChildPID, samples,
          stableSamples, candidateBytesMatch, promoted

vars == <<phase, source, schema, agentPID, childPID, previousPID, previousChildPID, samples,
         stableSamples, candidateBytesMatch, promoted>>

Init ==
  /\ phase = "checking"
  /\ source = Old
  /\ schema = 52
  /\ agentPID = 1
  /\ childPID = 1
  /\ previousPID = 0
  /\ previousChildPID = 0
  /\ samples = 0
  /\ stableSamples = 0
  /\ candidateBytesMatch = TRUE
  /\ promoted = FALSE

ObserveCurrent ==
  /\ phase = "checking"
  /\ samples < 5
  /\ LET nextStable == IF agentPID = previousPID /\ childPID = previousChildPID
                          THEN stableSamples + 1 ELSE 1
     nextSamples == samples + 1
     IN
       /\ samples' = nextSamples
       /\ stableSamples' = nextStable
       /\ previousPID' = agentPID
       /\ previousChildPID' = childPID
       /\ phase' = IF nextStable >= 3 THEN "admitted"
                    ELSE IF nextSamples >= 5 THEN "held" ELSE "checking"
  /\ UNCHANGED <<source, schema, agentPID, childPID, candidateBytesMatch, promoted>>

ObserveMissingAgent ==
  /\ phase = "checking"
  /\ samples < 5
  /\ samples' = samples + 1
  /\ stableSamples' = 0
  /\ previousPID' = 0
  /\ previousChildPID' = 0
  /\ phase' = IF samples + 1 >= 5 THEN "held" ELSE "checking"
  /\ UNCHANGED <<source, schema, agentPID, childPID, candidateBytesMatch, promoted>>

LaunchdRestarts ==
  /\ phase = "checking"
  /\ agentPID' = IF agentPID = 1 THEN 2 ELSE 1
  /\ childPID' = IF childPID = 1 THEN 2 ELSE 1
  /\ UNCHANGED <<phase, source, schema, previousPID, previousChildPID, samples,
                  stableSamples, candidateBytesMatch, promoted>>

Promote ==
  /\ phase = "admitted"
  /\ candidateBytesMatch
  /\ stableSamples >= 3
  /\ phase' = "promoted"
  /\ promoted' = TRUE
  /\ UNCHANGED <<source, schema, agentPID, childPID, previousPID, previousChildPID, samples,
                  stableSamples, candidateBytesMatch>>

HeldWait == /\ phase = "held" /\ UNCHANGED vars
AdmittedWait == /\ phase = "admitted" /\ UNCHANGED vars
PromotedWait == /\ phase = "promoted" /\ UNCHANGED vars

Next == ObserveCurrent \/ ObserveMissingAgent \/ LaunchdRestarts \/ Promote \/ HeldWait \/ AdmittedWait \/ PromotedWait
Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in Phases
  /\ source = Old
  /\ schema = 52
  /\ agentPID \in {1, 2}
  /\ childPID \in {1, 2}
  /\ previousPID \in {0, 1, 2}
  /\ previousChildPID \in {0, 1, 2}
  /\ samples \in 0..5
  /\ stableSamples \in 0..3
  /\ candidateBytesMatch = TRUE
  /\ promoted \in BOOLEAN

AdmissionRequiresStableExactAgent ==
  phase \in {"admitted", "promoted"} => (stableSamples = 3 /\ candidateBytesMatch)

NoPromotionBeforeAdmission == promoted => phase = "promoted" /\ stableSamples = 3

HeldLeavesSourceUntouched == phase = "held" => (source = Old /\ schema = 52 /\ ~promoted)

=============================================================================
