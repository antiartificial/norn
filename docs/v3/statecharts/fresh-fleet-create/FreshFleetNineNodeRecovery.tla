-------------------------- MODULE FreshFleetNineNodeRecovery --------------------------
EXTENDS Naturals, FiniteSets, TLC

Nodes == {"control-01", "control-02", "control-03",
          "ingress-01", "ingress-02",
          "app-01", "app-02", "app-03", "app-04"}
OrderedNodes == <<"control-01", "control-02", "control-03",
                 "ingress-01", "ingress-02",
                 "app-01", "app-02", "app-03", "app-04">>
ProviderID(n) == CASE n = "control-01" -> "do:5101"
                    [] n = "control-02" -> "do:5102"
                    [] n = "control-03" -> "do:5103"
                    [] n = "ingress-01" -> "do:5201"
                    [] n = "ingress-02" -> "do:5202"
                    [] n = "app-01" -> "do:5301"
                    [] n = "app-02" -> "do:5302"
                    [] n = "app-03" -> "do:5303"
                    [] OTHER -> "do:5304"
NextNode(s) == OrderedNodes[Cardinality(s) + 1]

VARIABLES phase, planReviewed, capacityBound, accountScoped, backendBound,
          sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
          applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
          applyRunner, recoveryRunner, controllerRunning,
          provider, state, providerIDs, stateIDs,
          providerReadback, stateReadback, providerReadbackIDs, stateReadbackIDs,
          failureReceipt, pkiReady, configured, terminalReceipt

vars == <<phase, planReviewed, capacityBound, accountScoped, backendBound,
          sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
          applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
          applyRunner, recoveryRunner, controllerRunning,
          provider, state, providerIDs, stateIDs,
          providerReadback, stateReadback, providerReadbackIDs, stateReadbackIDs,
          failureReceipt, pkiReady, configured, terminalReceipt>>

EmptyIDs == [n \in Nodes |-> "none"]
ExactReadback ==
    /\ providerReadback = provider
    /\ stateReadback = state
    /\ providerReadbackIDs = providerIDs
    /\ stateReadbackIDs = stateIDs
    /\ providerReadback = stateReadback
    /\ providerReadbackIDs = stateReadbackIDs

Init ==
    /\ phase = "inputs"
    /\ planReviewed = FALSE
    /\ capacityBound = FALSE
    /\ accountScoped = FALSE
    /\ backendBound = FALSE
    /\ sshPinned = FALSE
    /\ intentBound = FALSE
    /\ intentID = "none"
    /\ intentPlanSHA = "none"
    /\ intentStateLineage = "none"
    /\ stateLineage = "none"
    /\ applyNonceUsed = FALSE
    /\ recoveryNonceUsed = FALSE
    /\ recoveryPlanBound = FALSE
    /\ applyRunner = "not-started"
    /\ recoveryRunner = "not-started"
    /\ controllerRunning = TRUE
    /\ provider = {}
    /\ state = {}
    /\ providerIDs = EmptyIDs
    /\ stateIDs = EmptyIDs
    /\ providerReadback = {}
    /\ stateReadback = {}
    /\ providerReadbackIDs = EmptyIDs
    /\ stateReadbackIDs = EmptyIDs
    /\ failureReceipt = FALSE
    /\ pkiReady = FALSE
    /\ configured = {}
    /\ terminalReceipt = FALSE

ReviewPlan ==
    /\ phase = "inputs"
    /\ planReviewed' = TRUE
    /\ UNCHANGED <<capacityBound, accountScoped, backendBound, sshPinned,
                   intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, recoveryRunner, controllerRunning, provider,
                   state, providerIDs, stateIDs, providerReadback, stateReadback,
                   providerReadbackIDs, stateReadbackIDs, failureReceipt,
                   pkiReady, configured, terminalReceipt, phase>>

BindCapacityAndState ==
    /\ phase = "inputs"
    /\ capacityBound' = TRUE
    /\ backendBound' = TRUE
    /\ stateLineage' = "state-lineage-7"
    /\ UNCHANGED <<planReviewed, accountScoped, sshPinned, intentBound,
                   intentID, intentPlanSHA, intentStateLineage, applyNonceUsed, recoveryNonceUsed,
                   recoveryPlanBound, applyRunner, recoveryRunner,
                   controllerRunning, provider, state, providerIDs, stateIDs,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt, phase>>

AttestProviderAndSSH ==
    /\ phase = "inputs"
    /\ accountScoped' = TRUE
    /\ sshPinned' = TRUE
    /\ UNCHANGED <<planReviewed, capacityBound, backendBound, intentBound,
                   intentID, intentPlanSHA, stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, applyRunner,
                   recoveryRunner, controllerRunning, provider, state,
                   providerIDs, stateIDs, providerReadback, stateReadback,
                   providerReadbackIDs, stateReadbackIDs, failureReceipt,
                   pkiReady, configured, terminalReceipt, phase>>

BindIntent ==
    /\ phase = "inputs"
    /\ planReviewed /\ capacityBound /\ accountScoped /\ backendBound /\ sshPinned
    /\ intentBound' = TRUE
    /\ intentID' = "fresh-fleet-intent-1"
    /\ intentPlanSHA' = "sha256:reviewed-nine-node-plan"
    /\ intentStateLineage' = stateLineage
    /\ phase' = "ready"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, stateLineage, applyNonceUsed, recoveryNonceUsed,
                   recoveryPlanBound, applyRunner, recoveryRunner,
                   controllerRunning, provider, state, providerIDs, stateIDs,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt>>

AdmitApply ==
    /\ phase = "ready"
    /\ intentBound
    /\ ~applyNonceUsed
    /\ applyNonceUsed' = TRUE
    /\ applyRunner' = "active"
    /\ phase' = "applying"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   recoveryNonceUsed, recoveryPlanBound, recoveryRunner,
                   controllerRunning, provider, state, providerIDs, stateIDs,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt>>

CreateFirstEight(n) ==
    /\ phase = "applying"
    /\ controllerRunning
    /\ applyRunner = "active"
    /\ Cardinality(provider) < 8
    /\ n = NextNode(provider)
    /\ n \in Nodes \ provider
    /\ provider' = provider \cup {n}
    /\ state' = state \cup {n}
    /\ providerIDs' = [providerIDs EXCEPT ![n] = ProviderID(n)]
    /\ stateIDs' = [stateIDs EXCEPT ![n] = ProviderID(n)]
    /\ UNCHANGED <<phase, planReviewed, capacityBound, accountScoped,
                   backendBound, sshPinned, intentBound, intentID,
                   intentPlanSHA, stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, applyRunner,
                   recoveryRunner, controllerRunning, providerReadback,
                   stateReadback, providerReadbackIDs, stateReadbackIDs,
                   failureReceipt, pkiReady, configured, terminalReceipt>>

LoseApplyResponse ==
    /\ phase = "applying"
    /\ Cardinality(provider) = 8
    /\ applyRunner = "active"
    /\ phase' = "held"
    /\ applyRunner' = "retired"
    /\ controllerRunning' = FALSE
    /\ failureReceipt' = TRUE
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   recoveryRunner, provider, state, providerIDs, stateIDs,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, pkiReady, configured, terminalReceipt>>

LoseStateWriteResponse ==
    /\ phase = "applying"
    /\ Cardinality(provider) = 8
    /\ applyRunner = "active"
    /\ phase' = "held"
    /\ applyRunner' = "retired"
    /\ controllerRunning' = FALSE
    /\ failureReceipt' = TRUE
    /\ state' = state \ {"app-03"}
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA,
                   stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, recoveryRunner,
                   provider, providerIDs, stateIDs, providerReadback,
                   stateReadback, providerReadbackIDs, stateReadbackIDs,
                   pkiReady, configured, terminalReceipt>>

RestartController ==
    /\ phase = "held"
    /\ failureReceipt
    /\ applyRunner = "retired"
    /\ ~controllerRunning
    /\ controllerRunning' = TRUE
    /\ UNCHANGED <<phase, planReviewed, capacityBound, accountScoped,
                   backendBound, sshPinned, intentBound, intentID,
                   intentPlanSHA, stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, applyRunner,
                   recoveryRunner, provider, state, providerIDs, stateIDs,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt>>

ReadBackPartial ==
    /\ phase = "held"
    /\ controllerRunning
    /\ providerReadback' = provider
    /\ stateReadback' = state
    /\ providerReadbackIDs' = providerIDs
    /\ stateReadbackIDs' = stateIDs
    /\ UNCHANGED <<phase, planReviewed, capacityBound, accountScoped,
                   backendBound, sshPinned, intentBound, intentID,
                   intentPlanSHA, stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, applyRunner,
                   recoveryRunner, controllerRunning, provider, state,
                   providerIDs, stateIDs, failureReceipt, pkiReady,
                   configured, terminalReceipt>>

AdmitSameIntentRecovery ==
    /\ phase = "held"
    /\ controllerRunning
    /\ failureReceipt
    /\ applyRunner = "retired"
    /\ intentBound
    /\ ExactReadback
    /\ provider = state
    /\ Cardinality(provider) = 8
    /\ ~recoveryNonceUsed
    /\ recoveryNonceUsed' = TRUE
    /\ recoveryPlanBound' = TRUE
    /\ recoveryRunner' = "active"
    /\ phase' = "recovering"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, applyRunner, controllerRunning, provider,
                   state, providerIDs, stateIDs, providerReadback, stateReadback,
                   providerReadbackIDs, stateReadbackIDs, failureReceipt,
                   pkiReady, configured, terminalReceipt>>

CreateNinthFromBoundRemainder ==
    /\ phase = "recovering"
    /\ recoveryRunner = "active"
    /\ recoveryPlanBound
    /\ Cardinality(provider) = 8
    /\ LET n == NextNode(provider) IN
         /\ n = "app-04"
         /\ provider' = provider \cup {n}
         /\ state' = state \cup {n}
         /\ providerIDs' = [providerIDs EXCEPT ![n] = ProviderID(n)]
         /\ stateIDs' = [stateIDs EXCEPT ![n] = ProviderID(n)]
    /\ phase' = "recovering"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, recoveryRunner, controllerRunning,
                   providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt>>

ReadBackComplete ==
    /\ phase = "recovering"
    /\ provider = Nodes
    /\ state = Nodes
    /\ providerIDs = stateIDs
    /\ providerReadback' = provider
    /\ stateReadback' = state
    /\ providerReadbackIDs' = providerIDs
    /\ stateReadbackIDs' = stateIDs
    /\ phase' = "created"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, recoveryRunner, controllerRunning, provider,
                   state, providerIDs, stateIDs, failureReceipt, pkiReady,
                   configured, terminalReceipt>>

PreparePKIAndBootstrap ==
    /\ phase = "created"
    /\ ExactReadback
    /\ provider = Nodes
    /\ state = Nodes
    /\ providerIDs = stateIDs
    /\ pkiReady' = TRUE
    /\ phase' = "configuring"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, recoveryRunner, controllerRunning, provider,
                   state, providerIDs, stateIDs, providerReadback,
                   stateReadback, providerReadbackIDs, stateReadbackIDs,
                   failureReceipt, configured, terminalReceipt>>

ConfigureNextNode ==
    /\ phase = "configuring"
    /\ pkiReady
    /\ Cardinality(configured) < Cardinality(Nodes)
    /\ LET n == NextNode(configured) IN
         /\ n \in state
         /\ configured' = configured \cup {n}
    /\ UNCHANGED <<phase, planReviewed, capacityBound, accountScoped,
                   backendBound, sshPinned, intentBound, intentID,
                   intentPlanSHA, stateLineage, intentStateLineage, applyNonceUsed,
                   recoveryNonceUsed, recoveryPlanBound, applyRunner,
                   recoveryRunner, controllerRunning, provider, state,
                   providerIDs, stateIDs, providerReadback, stateReadback,
                   providerReadbackIDs, stateReadbackIDs, failureReceipt,
                   pkiReady, terminalReceipt>>

RetireRecoveryRunner ==
    /\ phase = "configuring"
    /\ configured = Nodes
    /\ recoveryRunner = "active"
    /\ recoveryRunner' = "retired"
    /\ phase' = "finalizing"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, controllerRunning, provider, state, providerIDs,
                   stateIDs, providerReadback, stateReadback, providerReadbackIDs,
                   stateReadbackIDs, failureReceipt, pkiReady, configured,
                   terminalReceipt>>

WriteTerminalReceipt ==
    /\ phase = "finalizing"
    /\ recoveryRunner = "retired"
    /\ ExactReadback
    /\ provider = Nodes
    /\ state = Nodes
    /\ configured = Nodes
    /\ terminalReceipt' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<planReviewed, capacityBound, accountScoped, backendBound,
                   sshPinned, intentBound, intentID, intentPlanSHA, stateLineage, intentStateLineage,
                   applyNonceUsed, recoveryNonceUsed, recoveryPlanBound,
                   applyRunner, recoveryRunner, controllerRunning, provider,
                   state, providerIDs, stateIDs, providerReadback, stateReadback,
                   providerReadbackIDs, stateReadbackIDs, failureReceipt,
                   pkiReady, configured>>

Next == AdmitApply \/ ReviewPlan \/ BindCapacityAndState \/ AttestProviderAndSSH
        \/ BindIntent \/ \E n \in Nodes: CreateFirstEight(n)
        \/ LoseApplyResponse \/ LoseStateWriteResponse
        \/ RestartController \/ ReadBackPartial
        \/ AdmitSameIntentRecovery \/ CreateNinthFromBoundRemainder
        \/ ReadBackComplete \/ PreparePKIAndBootstrap \/ ConfigureNextNode
        \/ RetireRecoveryRunner \/ WriteTerminalReceipt

DoneStutter == phase = "complete" /\ UNCHANGED vars
Spec == Init /\ [][Next \/ DoneStutter]_vars

TypeOK ==
    /\ phase \in {"inputs", "ready", "applying", "held", "recovering",
                   "created", "configuring", "finalizing", "complete"}
    /\ planReviewed \in BOOLEAN
    /\ capacityBound \in BOOLEAN
    /\ accountScoped \in BOOLEAN
    /\ backendBound \in BOOLEAN
    /\ sshPinned \in BOOLEAN
    /\ intentBound \in BOOLEAN
    /\ applyNonceUsed \in BOOLEAN
    /\ recoveryNonceUsed \in BOOLEAN
    /\ recoveryPlanBound \in BOOLEAN
    /\ applyRunner \in {"not-started", "active", "retired"}
    /\ recoveryRunner \in {"not-started", "active", "retired"}
    /\ controllerRunning \in BOOLEAN
    /\ provider \subseteq Nodes
    /\ state \subseteq Nodes
    /\ DOMAIN providerIDs = Nodes
    /\ DOMAIN stateIDs = Nodes
    /\ providerReadback \subseteq Nodes
    /\ stateReadback \subseteq Nodes
    /\ configured \subseteq Nodes
    /\ failureReceipt \in BOOLEAN
    /\ pkiReady \in BOOLEAN
    /\ terminalReceipt \in BOOLEAN

EffectsRequireBoundIntent == provider # {} => (intentBound /\ applyNonceUsed)
StateUsesExactProviderIDs == \A n \in state: stateIDs[n] = providerIDs[n]
ReadBackMustMatchObserved == ExactReadback =>
    (providerReadback = provider /\ stateReadback = state /\ providerReadbackIDs = providerIDs /\ stateReadbackIDs = stateIDs)
IntentBindsReviewedPlanAndState == intentBound =>
    (planReviewed /\ intentPlanSHA = "sha256:reviewed-nine-node-plan" /\
     stateLineage # "none" /\ intentStateLineage = stateLineage)
RecoveryUsesSameIntent == recoveryNonceUsed =>
    (intentBound /\ intentID = "fresh-fleet-intent-1" /\
     intentPlanSHA = "sha256:reviewed-nine-node-plan" /\
     intentStateLineage = stateLineage /\ recoveryPlanBound /\
     failureReceipt /\ applyRunner = "retired")
BootstrapRequiresAllNineExact == pkiReady =>
    (provider = Nodes /\ state = Nodes /\ providerIDs = stateIDs /\ ExactReadback)
NoConfigureWithoutBootstrapEvidence == configured # {} => (pkiReady /\ provider = Nodes /\ state = Nodes)
TerminalReceiptRequiresRunnerRetirement == terminalReceipt =>
    (phase = "complete" /\ recoveryRunner = "retired" /\ configured = Nodes /\ provider = Nodes /\ state = Nodes)
NoEffectsWhileHeld == phase = "held" =>
    (applyRunner = "retired" /\ recoveryRunner # "active")

=============================================================================
