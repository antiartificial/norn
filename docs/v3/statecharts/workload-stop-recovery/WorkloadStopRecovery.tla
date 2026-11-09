-------------------------- MODULE WorkloadStopRecovery --------------------------
EXTENDS Naturals, TLC

VARIABLES phase, intentPinned, targetHealthy, mixedRouteProven,
          stopWrites, stopMarker, stopReceipt, oldAllocationsTerminal,
          controllerAlive

vars == <<phase, intentPinned, targetHealthy, mixedRouteProven,
          stopWrites, stopMarker, stopReceipt, oldAllocationsTerminal,
          controllerAlive>>

Init ==
    /\ phase = "planned"
    /\ intentPinned = TRUE
    /\ targetHealthy = FALSE
    /\ mixedRouteProven = FALSE
    /\ stopWrites = 0
    /\ stopMarker = FALSE
    /\ stopReceipt = FALSE
    /\ oldAllocationsTerminal = FALSE
    /\ controllerAlive = TRUE

ObserveHealthyTarget ==
    /\ phase = "planned"
    /\ intentPinned
    /\ targetHealthy' = TRUE
    /\ UNCHANGED <<phase, intentPinned, mixedRouteProven, stopWrites,
                   stopMarker, stopReceipt, oldAllocationsTerminal,
                   controllerAlive>>

ProveMixedRoute ==
    /\ phase = "planned"
    /\ intentPinned
    /\ targetHealthy
    /\ mixedRouteProven' = TRUE
    /\ UNCHANGED <<phase, intentPinned, targetHealthy, stopWrites,
                   stopMarker, stopReceipt, oldAllocationsTerminal,
                   controllerAlive>>

ReserveStop ==
    /\ phase = "planned"
    /\ intentPinned
    /\ targetHealthy
    /\ mixedRouteProven
    /\ phase' = "reserved"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, stopReceipt,
                   oldAllocationsTerminal, controllerAlive>>

AcceptStopCAS ==
    /\ phase = "reserved"
    /\ stopWrites = 0
    /\ stopWrites' = 1
    /\ stopMarker' = TRUE
    /\ phase' = "awaiting-response"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopReceipt, oldAllocationsTerminal, controllerAlive>>

ReceiveStopResponse ==
    /\ phase = "awaiting-response"
    /\ stopMarker
    /\ stopReceipt' = TRUE
    /\ phase' = "draining"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, oldAllocationsTerminal,
                   controllerAlive>>

LoseStopResponse ==
    /\ phase = "awaiting-response"
    /\ stopMarker
    /\ controllerAlive' = FALSE
    /\ phase' = "recovering"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, stopReceipt,
                   oldAllocationsTerminal>>

ResumeSameIntent ==
    /\ phase = "recovering"
    /\ intentPinned
    /\ controllerAlive' = TRUE
    /\ phase' = "reconciling"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, stopReceipt,
                   oldAllocationsTerminal>>

RecoverByMarker ==
    /\ phase = "reconciling"
    /\ stopMarker
    /\ stopWrites = 1
    /\ stopReceipt' = TRUE
    /\ phase' = "draining"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, oldAllocationsTerminal,
                   controllerAlive>>

AllocationsBecomeTerminal ==
    /\ phase = "draining"
    /\ stopReceipt
    /\ oldAllocationsTerminal' = TRUE
    /\ UNCHANGED <<phase, intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, stopReceipt, controllerAlive>>

Complete ==
    /\ phase = "draining"
    /\ stopReceipt
    /\ stopMarker
    /\ oldAllocationsTerminal
    /\ phase' = "complete"
    /\ UNCHANGED <<intentPinned, targetHealthy, mixedRouteProven,
                   stopWrites, stopMarker, stopReceipt,
                   oldAllocationsTerminal, controllerAlive>>

StutterComplete == phase = "complete" /\ UNCHANGED vars

Next == ObserveHealthyTarget \/ ProveMixedRoute \/ ReserveStop \/ AcceptStopCAS
        \/ ReceiveStopResponse \/ LoseStopResponse \/ ResumeSameIntent
        \/ RecoverByMarker \/ AllocationsBecomeTerminal \/ Complete

Spec == Init /\ [][Next \/ StutterComplete]_vars

StopWrittenAtMostOnce == stopWrites \in {0, 1}
MarkerRequiresOneWrite == stopMarker => stopWrites = 1
ReceiptRequiresIntentMarker == stopReceipt => (stopMarker /\ intentPinned)
RecoveryDoesNotRewriteStop == phase = "draining" => stopWrites = 1
StopRequiresHealthyMixedRoute == stopWrites > 0 => (targetHealthy /\ mixedRouteProven)
CompletionRequiresTerminalAllocations == phase = "complete" =>
    (stopReceipt /\ stopMarker /\ oldAllocationsTerminal)

=================================================================================
