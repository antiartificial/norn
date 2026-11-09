-------------------------- MODULE WorkloadMixedVersion --------------------------
EXTENDS Naturals, TLC

CONSTANT AllocationCapacity

VARIABLES phase, intent, compatible, schemaExpanded, schemaContracted,
          oldAlloc, newAlloc, readyNew, route, routeReceipt,
          routeEvidenceRecorded, drainAck, oldWriters,
          controllerAlive, held, done

vars == <<phase, intent, compatible, schemaExpanded, schemaContracted,
          oldAlloc, newAlloc, readyNew, route, routeReceipt,
          routeEvidenceRecorded, drainAck, oldWriters,
          controllerAlive, held, done>>

Init ==
    /\ phase = "current"
    /\ intent = "none"
    /\ compatible = FALSE
    /\ schemaExpanded = FALSE
    /\ schemaContracted = FALSE
    /\ oldAlloc = 2
    /\ newAlloc = 0
    /\ readyNew = 0
    /\ route = "old"
    /\ routeReceipt = FALSE
    /\ routeEvidenceRecorded = FALSE
    /\ drainAck = FALSE
    /\ oldWriters = TRUE
    /\ controllerAlive = TRUE
    /\ held = FALSE
    /\ done = FALSE

AdmitIntent ==
    /\ phase = "current"
    /\ intent' = "exact-signed-target"
    /\ compatible' = TRUE
    /\ phase' = "planned"
    /\ UNCHANGED <<schemaExpanded, schemaContracted, oldAlloc, newAlloc,
                   readyNew, route, routeReceipt, routeEvidenceRecorded,
                   drainAck, oldWriters, controllerAlive, held, done>>

RejectBadIntent ==
    /\ phase = "current"
    /\ intent' = "rejected"
    /\ held' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<compatible, schemaExpanded, schemaContracted, oldAlloc,
                   newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, done>>

ExpandSchema ==
    /\ phase = "planned"
    /\ intent = "exact-signed-target"
    /\ compatible
    /\ schemaExpanded' = TRUE
    /\ phase' = "expanded"
    /\ UNCHANGED <<intent, compatible, schemaContracted, oldAlloc, newAlloc,
                   readyNew, route, routeReceipt, routeEvidenceRecorded,
                   drainAck, oldWriters, controllerAlive, held, done>>

StartTarget ==
    /\ phase \in {"expanded", "mixed"}
    /\ schemaExpanded
    /\ compatible
    /\ (~routeEvidenceRecorded \/ routeReceipt)
    /\ newAlloc < 2
    /\ oldAlloc + newAlloc < AllocationCapacity
    /\ newAlloc' = newAlloc + 1
    /\ phase' = "warming"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, held, done>>

TargetReady ==
    /\ phase = "warming"
    /\ readyNew < newAlloc
    /\ readyNew' = readyNew + 1
    /\ phase' = "mixed"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, held, done>>

PublishMixedRoute ==
    /\ phase = "mixed"
    /\ readyNew >= 1
    /\ compatible
    /\ route = "old"
    /\ route' = "mixed"
    /\ routeReceipt' = TRUE
    /\ routeEvidenceRecorded' = TRUE
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   phase, oldAlloc, newAlloc, readyNew, drainAck, oldWriters, controllerAlive, held, done>>

AcknowledgeDrain ==
    /\ phase = "mixed"
    /\ ~held
    /\ controllerAlive
    /\ readyNew + oldAlloc >= 3
    /\ route = "target"
    /\ routeReceipt
    /\ readyNew >= 1
    /\ oldAlloc > 0
    /\ drainAck' = TRUE
    /\ phase' = "draining"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, oldWriters, controllerAlive,
                   held, done>>

RetireOld ==
    /\ phase = "draining"
    /\ drainAck
    /\ routeReceipt
    /\ readyNew >= 1
    /\ oldAlloc > 0
    /\ oldAlloc' = oldAlloc - 1
    /\ drainAck' = FALSE
    /\ phase' = "mixed"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, oldWriters, controllerAlive,
                   held, done>>

PublishTargetRoute ==
    /\ phase = "mixed"
    /\ newAlloc = 2
    /\ readyNew = 2
    /\ compatible
    /\ route = "mixed"
    /\ routeReceipt
    /\ route' = "target"
    /\ routeReceipt' = TRUE
    /\ routeEvidenceRecorded' = TRUE
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   phase, oldAlloc, newAlloc, readyNew, routeEvidenceRecorded,
                   drainAck, oldWriters, controllerAlive, held, done>>

QuiesceOldWriters ==
    /\ phase = "mixed"
    /\ ~held
    /\ controllerAlive
    /\ oldAlloc = 0
    /\ newAlloc = 2
    /\ readyNew = 2
    /\ route = "target"
    /\ routeReceipt
    /\ oldWriters
    /\ oldWriters' = FALSE
    /\ phase' = "observing"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, controllerAlive,
                   held, done>>

ContractSchema ==
    /\ phase = "observing"
    /\ oldAlloc = 0
    /\ ~oldWriters
    /\ routeReceipt
    /\ schemaContracted' = TRUE
    /\ phase' = "contracted"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, oldAlloc, newAlloc,
                   readyNew, route, routeReceipt, routeEvidenceRecorded,
                   drainAck, oldWriters, controllerAlive, held, done>>

Complete ==
    /\ phase \in {"observing", "contracted"}
    /\ oldAlloc = 0
    /\ newAlloc = 2
    /\ readyNew = 2
    /\ route = "target"
    /\ routeReceipt
    /\ ~oldWriters
    /\ done' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, held>>

TargetFails ==
    /\ phase = "warming"
    /\ held' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, done>>

TargetAllocationLost ==
    /\ phase = "mixed"
    /\ route = "mixed"
    /\ routeReceipt
    /\ readyNew > 1
    /\ oldAlloc > 0
    /\ readyNew' = readyNew - 1
    /\ routeReceipt' = FALSE
    /\ held' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, route, routeEvidenceRecorded,
                   drainAck, oldWriters, controllerAlive, done>>

RouteEvidenceLost ==
    /\ ~done
    /\ route \in {"mixed", "target"}
    /\ routeReceipt
    /\ routeReceipt' = FALSE
    /\ held' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, done>>

ReconfirmCurrentRoute ==
    /\ phase \in {"mixed", "draining", "observing", "contracted", "held"}
    /\ intent = "exact-signed-target"
    /\ compatible
    /\ ~routeReceipt
    /\ routeEvidenceRecorded
    /\ route \in {"mixed", "target"}
    /\ IF route = "mixed"
          THEN oldAlloc > 0 /\ readyNew >= 1
          ELSE newAlloc = 2 /\ readyNew = 2
    /\ routeReceipt' = TRUE
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   phase, oldAlloc, newAlloc, readyNew, route,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, held, done>>

ControllerCrash ==
    /\ controllerAlive
    /\ controllerAlive' = FALSE
    /\ held' = TRUE
    /\ phase' = "held"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters, done>>

ResumeSameIntent ==
    /\ phase = "held"
    /\ intent = "exact-signed-target"
    /\ (~controllerAlive \/ routeReceipt)
    /\ (routeEvidenceRecorded => routeReceipt)
    /\ controllerAlive' = TRUE
    /\ held' = FALSE
    /\ phase' = "recovering"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters, done>>

ReconcileExactState ==
    /\ phase = "recovering"
    /\ intent = "exact-signed-target"
    /\ phase' = IF oldAlloc = 0 THEN "observing" ELSE
                  IF drainAck THEN "draining" ELSE
                  IF schemaExpanded THEN
                    IF newAlloc = 0 THEN "expanded" ELSE "mixed"
                  ELSE "planned"
    /\ UNCHANGED <<intent, compatible, schemaExpanded, schemaContracted,
                   oldAlloc, newAlloc, readyNew, route, routeReceipt,
                   routeEvidenceRecorded, drainAck, oldWriters,
                   controllerAlive, held, done>>

Next == AdmitIntent \/ RejectBadIntent \/ ExpandSchema \/ StartTarget
        \/ TargetReady \/ PublishMixedRoute \/ AcknowledgeDrain \/ RetireOld
        \/ PublishTargetRoute \/ QuiesceOldWriters \/ ContractSchema \/ Complete
        \/ TargetFails \/ TargetAllocationLost \/ RouteEvidenceLost
        \/ ReconfirmCurrentRoute \/ ControllerCrash
        \/ ResumeSameIntent \/ ReconcileExactState

DoneStutter == phase \in {"complete", "held"} /\ UNCHANGED vars
Spec == Init /\ [][Next \/ DoneStutter]_vars

CapacityBound == oldAlloc + newAlloc <= AllocationCapacity
AllocationBounds == oldAlloc \in 0..2 /\ newAlloc \in 0..2
ReadyBound == readyNew <= newAlloc
RouteHasReadyTarget == route \in {"old", "mixed", "target"} /\ (route = "old" \/ readyNew > 0)
NoOldRetirementBeforeRouteProof == oldAlloc = 2 \/ routeEvidenceRecorded
NoOldRetirementBeforeTargetOnlyRoute == oldAlloc = 2 \/ route = "target"
NoTargetRouteWithoutFullReadiness == route = "target" => (newAlloc = 2 /\ readyNew = 2)
MixedVersionsRequireCompatibility == (oldAlloc > 0 /\ newAlloc > 0) => (schemaExpanded /\ compatible)
NoSchemaContractionWithOldWriters == schemaContracted => ~oldWriters
TerminalStateSafe == done => (oldAlloc = 0 /\ newAlloc = 2 /\ readyNew = 2 /\ ~oldWriters /\ route = "target" /\ routeReceipt)
HoldIsExplicit == held => phase \in {"held", "recovering", "complete"}

=============================================================================
