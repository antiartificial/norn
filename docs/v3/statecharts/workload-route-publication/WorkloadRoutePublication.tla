------------------------ MODULE WorkloadRoutePublication ------------------------
EXTENDS Naturals, FiniteSets, TLC

Nodes == {"ingress-a", "ingress-b"}

VARIABLES phase, accepted, claimLive, appLock, inventoryCurrent,
          targetHealthy, activeGeneration, routeGeneration, routeStep, published,
          routeReadback, trafficProof, receiptCommitted, stopAuthorized,
          publicationWrites, controllerAlive

vars == <<phase, accepted, claimLive, appLock, inventoryCurrent,
          targetHealthy, activeGeneration, routeGeneration, routeStep, published,
          routeReadback, trafficProof, receiptCommitted, stopAuthorized,
          publicationWrites, controllerAlive>>

Init ==
    /\ phase = "warming"
    /\ accepted = TRUE
    /\ claimLive = TRUE
    /\ appLock = TRUE
    /\ inventoryCurrent = TRUE
    /\ targetHealthy = TRUE
    /\ activeGeneration = 10
    /\ routeGeneration = 10
    /\ routeStep = 0
    /\ published = {}
    /\ routeReadback = FALSE
    /\ trafficProof = FALSE
    /\ receiptCommitted = FALSE
    /\ stopAuthorized = FALSE
    /\ publicationWrites = 0
    /\ controllerAlive = TRUE

ReserveMixedStep ==
    /\ phase = "warming"
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ targetHealthy
    /\ activeGeneration = 10
    /\ routeGeneration = 10
    /\ routeStep = 0
    /\ phase' = "publishing"
    /\ routeGeneration' = 11
    /\ routeStep' = 1
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, published, routeReadback,
                   trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

PublishOneNode(node) ==
    /\ phase = "publishing"
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ controllerAlive
    /\ activeGeneration = 10
    /\ node \in Nodes \ published
    /\ published' = published \cup {node}
    /\ publicationWrites' = publicationWrites + 1
    /\ UNCHANGED <<phase, accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, controllerAlive>>

LosePartialResponse ==
    /\ phase = "publishing"
    /\ published # {}
    /\ published # Nodes
    /\ controllerAlive
    /\ controllerAlive' = FALSE
    /\ phase' = "recovering"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites>>

InventoryDriftDuringRoute ==
    /\ phase \in {"publishing", "recovering", "reconciling", "proving-traffic", "committing"}
    /\ inventoryCurrent
    /\ inventoryCurrent' = FALSE
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, claimLive, appLock, targetHealthy,
                   activeGeneration, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

ResumeBeforeRouteCAS ==
    /\ phase = "recovering"
    /\ accepted
    /\ appLock
    /\ inventoryCurrent
    /\ activeGeneration = 10
    /\ routeGeneration = 11
    /\ routeStep = 1
    /\ controllerAlive = FALSE
    /\ controllerAlive' = TRUE
    /\ claimLive' = TRUE
    /\ routeReadback' = FALSE
    /\ trafficProof' = FALSE
    /\ phase' = "publishing"
    /\ UNCHANGED <<accepted, appLock, inventoryCurrent, targetHealthy,
                   activeGeneration, routeGeneration, routeStep, published,
                   receiptCommitted, stopAuthorized, publicationWrites>>

ReadEveryNode ==
    /\ phase = "publishing"
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ controllerAlive
    /\ activeGeneration = 10
    /\ published = Nodes
    /\ routeReadback' = TRUE
    /\ phase' = "proving-traffic"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

ProveTraffic ==
    /\ phase = "proving-traffic"
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ targetHealthy
    /\ routeReadback
    /\ trafficProof' = TRUE
    /\ phase' = IF receiptCommitted THEN "routed" ELSE "committing"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, routeReadback, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

AdvanceActiveRoute ==
    /\ phase = "committing"
    /\ activeGeneration = 10
    /\ routeGeneration = 11
    /\ routeReadback
    /\ trafficProof
    /\ activeGeneration' = 11
    /\ receiptCommitted' = TRUE
    /\ phase' = "routed"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, routeGeneration, routeStep, published,
                   routeReadback, trafficProof,
                   stopAuthorized, publicationWrites, controllerAlive>>

CompetingRouteChange ==
    /\ phase \in {"publishing", "proving-traffic", "committing"}
    /\ activeGeneration = 10
    /\ activeGeneration' = 12
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites, controllerAlive>>

CompetingRouteChangeAfterReceipt ==
    /\ phase = "stop-ready"
    /\ activeGeneration = 11
    /\ activeGeneration' = 12
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites, controllerAlive>>

LoseActiveCASResponse ==
    /\ phase = "routed"
    /\ receiptCommitted
    /\ controllerAlive
    /\ controllerAlive' = FALSE
    /\ phase' = "recovering"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites>>

ResumeAfterRouteCAS ==
    /\ phase = "recovering"
    /\ accepted
    /\ appLock
    /\ inventoryCurrent
    /\ activeGeneration = 11
    /\ routeGeneration = 11
    /\ routeStep = 1
    /\ controllerAlive = FALSE
    /\ controllerAlive' = TRUE
    /\ claimLive' = TRUE
    /\ phase' = "reconciling"
    /\ UNCHANGED <<accepted, appLock, inventoryCurrent, targetHealthy,
                   activeGeneration, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites>>

ReconfirmEveryNode ==
    /\ phase = "reconciling"
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ controllerAlive
    /\ activeGeneration = 11
    /\ routeGeneration = 11
    /\ published = Nodes
    /\ routeReadback' = TRUE
    /\ phase' = "proving-traffic"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

RevalidateBeforeOldStop ==
    /\ phase = "routed"
    /\ receiptCommitted
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ targetHealthy
    /\ activeGeneration = routeGeneration
    /\ routeReadback
    /\ trafficProof
    /\ phase' = "stop-ready"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, routeReadback, trafficProof, receiptCommitted,
                   stopAuthorized, publicationWrites, controllerAlive>>

AuthorizeOldStop ==
    /\ phase = "stop-ready"
    /\ receiptCommitted
    /\ accepted
    /\ claimLive
    /\ appLock
    /\ inventoryCurrent
    /\ targetHealthy
    /\ activeGeneration = routeGeneration
    /\ routeReadback
    /\ trafficProof
    /\ stopAuthorized' = TRUE
    /\ phase' = "stop-authorized"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, routeReadback, trafficProof, receiptCommitted,
                   publicationWrites, controllerAlive>>

InventoryDriftAfterReceipt ==
    /\ phase \in {"routed", "stop-ready"}
    /\ receiptCommitted
    /\ inventoryCurrent
    /\ inventoryCurrent' = FALSE
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, claimLive, appLock, targetHealthy,
                   activeGeneration, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

TargetHealthLost ==
    /\ phase \in {"publishing", "proving-traffic", "committing", "reconciling", "routed", "stop-ready"}
    /\ targetHealthy
    /\ targetHealthy' = FALSE
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   activeGeneration, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

ControllerCrashBeforeReceipt ==
    /\ phase \in {"publishing", "proving-traffic", "committing"}
    /\ activeGeneration = 10
    /\ controllerAlive
    /\ controllerAlive' = FALSE
    /\ routeReadback' = FALSE
    /\ trafficProof' = FALSE
    /\ phase' = "recovering"
    /\ UNCHANGED <<accepted, claimLive, appLock, inventoryCurrent,
                   targetHealthy, activeGeneration, routeGeneration, routeStep,
                   published, receiptCommitted, stopAuthorized, publicationWrites>>

LostAuthority ==
    /\ phase \in {"publishing", "recovering", "reconciling", "proving-traffic", "committing", "stop-ready"}
    /\ claimLive
    /\ claimLive' = FALSE
    /\ phase' = "held"
    /\ UNCHANGED <<accepted, appLock, inventoryCurrent, targetHealthy,
                   activeGeneration, routeGeneration, routeStep, published,
                   routeReadback, trafficProof, receiptCommitted, stopAuthorized,
                   publicationWrites, controllerAlive>>

Complete == phase = "stop-authorized" /\ stopAuthorized /\ UNCHANGED vars
Held == phase = "held" /\ UNCHANGED vars

Next == ReserveMixedStep
        \/ \E node \in Nodes : PublishOneNode(node)
        \/ LosePartialResponse \/ InventoryDriftDuringRoute
        \/ ResumeBeforeRouteCAS \/ ReadEveryNode \/ ProveTraffic
        \/ AdvanceActiveRoute \/ CompetingRouteChange \/ CompetingRouteChangeAfterReceipt
        \/ LoseActiveCASResponse \/ ResumeAfterRouteCAS \/ ReconfirmEveryNode
        \/ RevalidateBeforeOldStop \/ AuthorizeOldStop \/ InventoryDriftAfterReceipt
        \/ TargetHealthLost \/ ControllerCrashBeforeReceipt \/ LostAuthority

Spec == Init /\ [][Next \/ Complete \/ Held]_vars

NoReceiptWithoutIndependentProof == receiptCommitted => (routeReadback /\ trafficProof)
ActiveRouteAdvanceHasReceipt == activeGeneration = 11 /\ routeGeneration = 11 => receiptCommitted
NoTrafficBeforeEveryNodeReadback == trafficProof => (published = Nodes /\ routeReadback)
NoRouteWriteWithoutAuthority == publicationWrites > 0 => (accepted /\ routeGeneration = 11 /\ routeStep = 1)
SameStepRecoveryOnly == phase = "recovering" => (routeGeneration = 11 /\ routeStep = 1)
StopRequiresFreshInventoryProof == stopAuthorized => (receiptCommitted /\ inventoryCurrent)
StopRequiresCurrentRouteAndHealth == stopAuthorized => (targetHealthy /\ activeGeneration = routeGeneration /\ routeReadback /\ trafficProof)
NoDuplicateNodePublication == publicationWrites = Cardinality(published)

================================================================================
