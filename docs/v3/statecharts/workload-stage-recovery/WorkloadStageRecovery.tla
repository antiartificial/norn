-------------------------- MODULE WorkloadStageRecovery --------------------------
EXTENDS Naturals, TLC

VARIABLES phase, generation, marker, createAttempts, createPending,
          targetExists, targetObserved, targetBound, targetHealthy,
          oldServing, routeTarget, controllerAlive, terminal

vars == <<phase, generation, marker, createAttempts, createPending,
          targetExists, targetObserved, targetBound, targetHealthy,
          oldServing, routeTarget, controllerAlive, terminal>>

Init ==
    /\ phase = "accepted"
    /\ generation = 1
    /\ marker = FALSE
    /\ createAttempts = 0
    /\ createPending = FALSE
    /\ targetExists = FALSE
    /\ targetObserved = FALSE
    /\ targetBound = FALSE
    /\ targetHealthy = FALSE
    /\ oldServing = TRUE
    /\ routeTarget = FALSE
    /\ controllerAlive = TRUE
    /\ terminal = FALSE

ClaimOperation ==
    /\ phase = "accepted"
    /\ phase' = "claimed"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, routeTarget, controllerAlive, terminal>>

ReserveTargetEffect ==
    /\ phase = "claimed"
    /\ phase' = "reserved"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, routeTarget, controllerAlive, terminal>>

StagePrivateInputs ==
    /\ phase = "reserved"
    /\ phase' = "inputs-staged"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, routeTarget, controllerAlive, terminal>>

RecordCreateAttempt ==
    /\ phase = "inputs-staged"
    /\ marker = FALSE
    /\ marker' = TRUE
    /\ phase' = "attempt-recorded"
    /\ UNCHANGED <<generation, createAttempts, createPending, targetExists,
                   targetObserved, targetBound, targetHealthy, oldServing,
                   routeTarget, controllerAlive, terminal>>

SubmitCreate ==
    /\ phase = "attempt-recorded"
    /\ marker
    /\ createAttempts = 0
    /\ createAttempts' = 1
    /\ createPending' = TRUE
    /\ phase' = "awaiting-target"
    /\ UNCHANGED <<generation, marker, targetExists, targetObserved,
                   targetBound, targetHealthy, oldServing, routeTarget,
                   controllerAlive, terminal>>

NomadAcceptsCreate ==
    /\ createPending
    /\ ~targetExists
    /\ targetExists' = TRUE
    /\ createPending' = FALSE
    /\ phase' = IF controllerAlive THEN "target-created" ELSE phase
    /\ UNCHANGED <<generation, marker, createAttempts, targetObserved,
                   targetBound, targetHealthy, oldServing, routeTarget,
                   controllerAlive, terminal>>

ControllerCrashes ==
    /\ controllerAlive
    /\ generation = 1
    /\ phase \in {"attempt-recorded", "awaiting-target", "target-created"}
    /\ ~terminal
    /\ controllerAlive' = FALSE
    /\ phase' = "controller-stopped"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, routeTarget, terminal>>

SuccessorClaims ==
    /\ ~controllerAlive
    /\ ~terminal
    /\ controllerAlive' = TRUE
    /\ generation' = generation + 1
    /\ phase' = "reconciling"
    /\ UNCHANGED <<marker, createAttempts, createPending, targetExists,
                   targetObserved, targetBound, targetHealthy, oldServing,
                   routeTarget, terminal>>

ReadExactTarget ==
    /\ phase \in {"reconciling", "target-created"}
    /\ targetExists
    /\ targetObserved' = TRUE
    /\ phase' = "target-read-back"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetBound, targetHealthy, oldServing,
                   routeTarget, controllerAlive, terminal>>

ReadTargetAbsent ==
    /\ phase = "reconciling"
    /\ ~targetExists
    /\ targetObserved' = FALSE
    /\ UNCHANGED <<phase, generation, marker, createAttempts, createPending,
                   targetExists, targetBound, targetHealthy, oldServing,
                   routeTarget, controllerAlive, terminal>>

BindObservedTarget ==
    /\ phase = "target-read-back"
    /\ marker
    /\ targetExists
    /\ targetObserved
    /\ targetBound' = TRUE
    /\ phase' = "target-bound"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetHealthy, oldServing,
                   routeTarget, controllerAlive, terminal>>

ObserveHealthyTarget ==
    /\ phase = "target-bound"
    /\ targetBound
    /\ targetHealthy' = TRUE
    /\ phase' = "target-healthy"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, oldServing,
                   routeTarget, controllerAlive, terminal>>

PublishTargetRoute ==
    /\ phase = "target-healthy"
    /\ targetHealthy
    /\ oldServing
    /\ routeTarget' = TRUE
    /\ phase' = "target-routed"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, controllerAlive, terminal>>

DrainOldAfterTargetRoute ==
    /\ phase = "target-routed"
    /\ routeTarget
    /\ targetBound
    /\ targetHealthy
    /\ oldServing
    /\ oldServing' = FALSE
    /\ phase' = "old-drained"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   routeTarget, controllerAlive, terminal>>

CompleteRelease ==
    /\ phase = "old-drained"
    /\ routeTarget
    /\ ~oldServing
    /\ terminal' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<generation, marker, createAttempts, createPending,
                   targetExists, targetObserved, targetBound, targetHealthy,
                   oldServing, routeTarget, controllerAlive>>

NoDuplicateCreate == createAttempts <= 1
CreateRequiresDurableMarker == createAttempts > 0 => marker
BoundTargetWasReadBack == targetBound => (marker /\ targetExists /\ targetObserved)
RouteRequiresHealthyBoundTarget == routeTarget => (targetBound /\ targetHealthy)
OldStopsOnlyAfterTargetRoute == ~oldServing => (routeTarget /\ targetBound /\ targetHealthy)
TerminalRequiresTargetRoute == terminal => routeTarget
NoTerminalWhileCreateUnresolved == terminal => ~createPending

Next == ClaimOperation \/ ReserveTargetEffect \/ StagePrivateInputs \/ RecordCreateAttempt \/
        SubmitCreate \/ NomadAcceptsCreate \/ ControllerCrashes \/ SuccessorClaims \/
        ReadExactTarget \/ ReadTargetAbsent \/ BindObservedTarget \/ ObserveHealthyTarget \/
        PublishTargetRoute \/ DrainOldAfterTargetRoute \/ CompleteRelease \/
        (terminal /\ UNCHANGED vars)

Spec == Init /\ [][Next]_vars

=============================================================================
