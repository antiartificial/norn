---------------- MODULE NornRelease ----------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANT Controls
ASSUME Controls = {1, 2, 3}
Old == "old"
Target == "target"
Third == "third"
VARIABLES phase, staged, stopped, promoted, started, startedWithTarget, versions,
          intentValid, runnerCount, journalDurable, healthy, pinsTarget,
          targetEverStarted, unknownObserved, heldFrom
vars == <<phase, staged, stopped, promoted, started, startedWithTarget, versions,
          intentValid, runnerCount, journalDurable, healthy, pinsTarget,
          targetEverStarted, unknownObserved, heldFrom>>
Init ==
  /\ phase = "intent-ready"
  /\ staged = {}
  /\ stopped = {}
  /\ promoted = {}
  /\ started = {}
  /\ startedWithTarget = {}
  /\ versions = [c \in Controls |-> Old]
  /\ intentValid \in BOOLEAN
  /\ runnerCount = 0
  /\ journalDurable = FALSE
  /\ healthy = FALSE
  /\ pinsTarget = FALSE
  /\ targetEverStarted = FALSE
  /\ unknownObserved = FALSE
  /\ heldFrom = "none"
Admit ==
  /\ phase = "intent-ready" /\ intentValid /\ runnerCount = 0
  /\ phase' = "staging" /\ runnerCount' = 1 /\ journalDurable' = TRUE
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  healthy, pinsTarget, targetEverStarted, unknownObserved, heldFrom>>
Stage(c) ==
  /\ phase = "staging" /\ c \in Controls \ staged
  /\ staged' = staged \cup {c}
  /\ phase' = IF staged' = Controls THEN "staged" ELSE phase
  /\ UNCHANGED <<stopped, promoted, started, startedWithTarget, versions, intentValid,
                  runnerCount, journalDurable, healthy, pinsTarget,
                  targetEverStarted, unknownObserved, heldFrom>>
BeginStop ==
  /\ phase = "staged" /\ staged = Controls
  /\ phase' = "stopping"
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  runnerCount, journalDurable, healthy, pinsTarget,
                  targetEverStarted, unknownObserved, heldFrom>>
Stop(c) ==
  /\ phase = "stopping" /\ c \in Controls \ stopped
  /\ stopped' = stopped \cup {c}
  /\ phase' = IF stopped' = Controls THEN "stopped" ELSE phase
  /\ UNCHANGED <<staged, promoted, started, startedWithTarget, versions, intentValid,
                  runnerCount, journalDurable, healthy, pinsTarget,
                  targetEverStarted, unknownObserved, heldFrom>>
Promote(c) ==
  /\ phase = "stopped" /\ stopped = Controls /\ c \in Controls \ promoted
  /\ promoted' = promoted \cup {c}
  /\ versions' = [versions EXCEPT ![c] = Target]
  /\ phase' = IF promoted' = Controls THEN "promoted" ELSE phase
  /\ UNCHANGED <<staged, stopped, started, startedWithTarget, intentValid, runnerCount,
                  journalDurable, healthy, pinsTarget, targetEverStarted,
                  unknownObserved, heldFrom>>
BeginStart ==
  /\ phase = "promoted" /\ promoted = Controls /\ stopped = Controls
  /\ phase' = "starting"
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  runnerCount, journalDurable, healthy, pinsTarget,
                  targetEverStarted, unknownObserved, heldFrom>>
Start(c) ==
  /\ phase = "starting" /\ c \in Controls \ started
  /\ versions[c] = Target
  /\ started' = started \cup {c}
  /\ startedWithTarget' = startedWithTarget \cup {c}
  /\ targetEverStarted' = TRUE
  /\ phase' = IF started' = Controls THEN "verify" ELSE phase
  /\ UNCHANGED <<staged, stopped, promoted, versions, intentValid,
                  runnerCount, journalDurable, healthy, pinsTarget,
                  unknownObserved, heldFrom>>
VerifyHealthy ==
  /\ phase = "verify" /\ started = Controls /\ versions = [c \in Controls |-> Target]
  /\ healthy' = TRUE /\ phase' = "healthy"
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  runnerCount, journalDurable, pinsTarget, targetEverStarted,
                  unknownObserved, heldFrom>>
PersistPins ==
  /\ phase = "healthy" /\ healthy /\ ~pinsTarget
  /\ pinsTarget' = TRUE /\ phase' = "complete" /\ runnerCount' = 0
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  journalDurable, healthy, targetEverStarted, unknownObserved, heldFrom>>
ObserveThird(c) ==
  /\ phase \notin {"complete", "held"} /\ c \in Controls
  /\ versions' = [versions EXCEPT ![c] = Third]
  /\ unknownObserved' = TRUE
  /\ phase' = "held" /\ heldFrom' = phase /\ runnerCount' = 0
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, intentValid,
                  journalDurable, healthy, pinsTarget, targetEverStarted>>
FailClosed ==
  /\ phase \notin {"complete", "held"}
  /\ phase' = "held" /\ heldFrom' = phase /\ runnerCount' = 0
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions, intentValid,
                  journalDurable, healthy, pinsTarget, targetEverStarted,
                  unknownObserved>>
ResumeSameIntent ==
  /\ phase = "held" /\ ~unknownObserved /\ intentValid /\ journalDurable
  /\ heldFrom \in {"staging", "staged", "stopping", "stopped", "promoted",
                     "starting", "verify", "healthy"}
  /\ phase' = heldFrom /\ runnerCount' = 1
  /\ UNCHANGED <<staged, stopped, promoted, started, startedWithTarget, versions,
                  intentValid, journalDurable, healthy, pinsTarget,
                  targetEverStarted, unknownObserved, heldFrom>>
Done == phase \in {"complete", "held"} /\ UNCHANGED vars
Next == Admit \/ FailClosed \/ ObserveThird(1) \/ ObserveThird(2) \/ ObserveThird(3)
        \/ \E c \in Controls: Stage(c) \/ Stop(c) \/ Promote(c) \/ Start(c)
        \/ BeginStop \/ BeginStart \/ VerifyHealthy \/ PersistPins \/ ResumeSameIntent \/ Done
NoMutationWithoutIntent ==
  (staged # {} \/ stopped # {} \/ promoted # {} \/ started # {} \/ pinsTarget)
    => intentValid
StopsRequireAllStaged == stopped # {} => staged = Controls
PromotesRequireAllStopped == promoted # {} => stopped = Controls
StartsRequireTargetPromoted == started # {} => promoted = Controls
TargetStartHasTargetBytes == started \subseteq startedWithTarget
PinsRequireHealthyTarget == pinsTarget => healthy /\ versions = [c \in Controls |-> Target]
RunnerIsOneUse == runnerCount \in {0, 1}
RunnerHasDurableIntent == runnerCount = 1 => intentValid /\ journalDurable
UnknownStateHolds == unknownObserved => phase = "held"
Spec == Init /\ [][Next]_vars
====================================================
