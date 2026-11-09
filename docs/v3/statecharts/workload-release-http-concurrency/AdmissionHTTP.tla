---------------- MODULE AdmissionHTTP ----------------
EXTENDS Naturals, TLC

CONSTANT Handlers

Phases == {"lookup", "derive", "accept", "done"}
Results == {"none", "fresh", "replay", "conflict"}
Selection == "qualification-1/replicas-2"

VARIABLES phase, accepted, acceptedSelection, result, freshAcceptances, mutableReads
vars == <<phase, accepted, acceptedSelection, result, freshAcceptances, mutableReads>>

Init ==
  /\ phase = [h \in Handlers |-> "lookup"]
  /\ accepted = FALSE
  /\ acceptedSelection = "none"
  /\ result = [h \in Handlers |-> "none"]
  /\ freshAcceptances = 0
  /\ mutableReads = 0

LookupReplayMiss(h) ==
  /\ h \in Handlers
  /\ phase[h] = "lookup"
  /\ ~accepted
  /\ phase' = [phase EXCEPT ![h] = "derive"]
  /\ UNCHANGED <<accepted, acceptedSelection, result, freshAcceptances, mutableReads>>

LookupReplayHit(h) ==
  /\ h \in Handlers
  /\ phase[h] = "lookup"
  /\ accepted
  /\ phase' = [phase EXCEPT ![h] = "done"]
  /\ result' = [result EXCEPT ![h] = "replay"]
  /\ UNCHANGED <<accepted, acceptedSelection, freshAcceptances, mutableReads>>

ReadMutableEvidence(h) ==
  /\ h \in Handlers
  /\ phase[h] = "derive"
  /\ phase' = [phase EXCEPT ![h] = "accept"]
  /\ mutableReads' = mutableReads + 1
  /\ UNCHANGED <<accepted, acceptedSelection, result, freshAcceptances>>

CommitFreshAcceptance(h) ==
  /\ h \in Handlers
  /\ phase[h] = "accept"
  /\ ~accepted
  /\ phase' = [phase EXCEPT ![h] = "done"]
  /\ accepted' = TRUE
  /\ acceptedSelection' = Selection
  /\ result' = [result EXCEPT ![h] = "fresh"]
  /\ freshAcceptances' = freshAcceptances + 1
  /\ UNCHANGED mutableReads

ResolveAcceptanceRace(h) ==
  /\ h \in Handlers
  /\ phase[h] = "accept"
  /\ accepted
  /\ phase' = [phase EXCEPT ![h] = "done"]
  /\ result' = [result EXCEPT ![h] = "replay"]
  /\ UNCHANGED <<accepted, acceptedSelection, freshAcceptances, mutableReads>>

Done ==
  /\ \A h \in Handlers : result[h] # "none"
  /\ UNCHANGED vars

Next ==
  \/ \E h \in Handlers : LookupReplayMiss(h)
  \/ \E h \in Handlers : LookupReplayHit(h)
  \/ \E h \in Handlers : ReadMutableEvidence(h)
  \/ \E h \in Handlers : CommitFreshAcceptance(h)
  \/ \E h \in Handlers : ResolveAcceptanceRace(h)
  \/ Done

Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ phase \in [Handlers -> Phases]
  /\ accepted \in BOOLEAN
  /\ acceptedSelection \in {"none", Selection}
  /\ result \in [Handlers -> Results]
  /\ freshAcceptances \in Nat
  /\ mutableReads \in Nat

AtMostOneFreshAcceptance == freshAcceptances <= 1
FreshAcceptanceHasStableSelection == accepted => acceptedSelection = Selection
EveryReplayReturnsAcceptedSelection ==
  \A h \in Handlers : result[h] = "replay" => accepted /\ acceptedSelection = Selection
EveryFreshResultHasDurableAcceptance ==
  \A h \in Handlers : result[h] = "fresh" => accepted /\ acceptedSelection = Selection
BothSuccessfulRequestsConverge ==
  (\A h \in Handlers : result[h] \in {"fresh", "replay"}) =>
    (\A h \in Handlers : result[h] \in {"fresh", "replay"}) /\ freshAcceptances = 1

THEOREM Spec => []TypeOK
====================================================
