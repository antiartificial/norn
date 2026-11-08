-------------------------- MODULE FreshFleetCreate --------------------------
EXTENDS Naturals, FiniteSets, TLC

Groups == {"control", "ingress", "app"}

VARIABLES phase, planValid, capacityBound, nonceValid, nonceUsed,
          accountValid, backendValid, sshReady, admitted, intentBound,
          provider, observed, configured, uncertain, pkiReady,
          cleanupApproved, cleanupAuthorized, terminal

vars == <<phase, planValid, capacityBound, nonceValid, nonceUsed,
          accountValid, backendValid, sshReady, admitted, intentBound,
          provider, observed, configured, uncertain, pkiReady,
          cleanupApproved, cleanupAuthorized, terminal>>

Init ==
    /\ phase = "inputs"
    /\ planValid = FALSE
    /\ capacityBound = FALSE
    /\ nonceValid = FALSE
    /\ nonceUsed = FALSE
    /\ accountValid = FALSE
    /\ backendValid = FALSE
    /\ sshReady = FALSE
    /\ admitted = FALSE
    /\ intentBound = FALSE
    /\ provider = {}
    /\ observed = {}
    /\ configured = {}
    /\ uncertain = {}
    /\ pkiReady = FALSE
    /\ cleanupApproved = FALSE
    /\ cleanupAuthorized = FALSE
    /\ terminal = FALSE

SupplyReviewedPlan ==
    /\ phase = "inputs"
    /\ planValid' = TRUE
    /\ UNCHANGED <<capacityBound, nonceValid, nonceUsed, accountValid,
                   backendValid, sshReady, admitted, intentBound, provider,
                   observed, configured, uncertain, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal, phase>>

BindCapacityIntent ==
    /\ phase = "inputs"
    /\ capacityBound' = TRUE
    /\ nonceValid' = TRUE
    /\ UNCHANGED <<planValid, nonceUsed, accountValid, backendValid,
                   sshReady, admitted, intentBound, provider, observed,
                   configured, uncertain, pkiReady, cleanupApproved,
                   cleanupAuthorized, terminal, phase>>

ProviderAccountPreflight ==
    /\ phase = "inputs"
    /\ accountValid' = TRUE
    /\ backendValid' = TRUE
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   sshReady, admitted, intentBound, provider, observed,
                   configured, uncertain, pkiReady, cleanupApproved,
                   cleanupAuthorized, terminal, phase>>

PrepareSSH ==
    /\ phase = "inputs"
    /\ sshReady' = TRUE
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, admitted, intentBound,
                   provider, observed, configured, uncertain, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal, phase>>

AdmitIntent ==
    /\ phase = "inputs"
    /\ planValid /\ capacityBound /\ nonceValid
    /\ accountValid /\ backendValid /\ sshReady
    /\ admitted' = TRUE
    /\ intentBound' = TRUE
    /\ nonceUsed' = TRUE
    /\ phase' = "ready-to-apply"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, accountValid,
                   backendValid, sshReady, provider, observed, configured,
                   uncertain, pkiReady, cleanupApproved,
                   cleanupAuthorized, terminal>>

RejectOrExpire ==
    /\ phase = "inputs"
    /\ phase' = "held"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, cleanupAuthorized, terminal>>

CreateGroup(g) ==
    /\ phase \in {"ready-to-apply", "creating"}
    /\ admitted /\ intentBound /\ nonceUsed
    /\ g \in Groups \ provider
    /\ provider' = provider \cup {g}
    /\ phase' = "creating"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, observed, configured, uncertain, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal>>

LoseCreateResponse(g) ==
    /\ phase = "creating"
    /\ g \in provider \ observed
    /\ uncertain' = uncertain \cup {g}
    /\ phase' = "held"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal>>

ReadProviderState ==
    /\ phase \in {"creating", "held", "recovering"}
    /\ observed' = provider
    /\ uncertain' = {}
    /\ phase' = IF phase \in {"held", "recovering"} THEN phase
                ELSE IF provider = Groups THEN "created" ELSE "creating"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, configured, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal>>

PreparePKI ==
    /\ phase = "created"
    /\ observed = Groups
    /\ pkiReady' = TRUE
    /\ phase' = "configuring"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   cleanupApproved, cleanupAuthorized, terminal>>

ConfigureGroup(g) ==
    /\ phase \in {"configuring", "configured"}
    /\ pkiReady
    /\ g \in observed \ configured
    /\ configured' = configured \cup {g}
    /\ phase' = IF configured' = Groups THEN "configured" ELSE "configuring"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, uncertain, pkiReady,
                   cleanupApproved, cleanupAuthorized, terminal>>

ApproveExactCleanup ==
    /\ phase = "held"
    /\ cleanupApproved' = TRUE
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupAuthorized, terminal, phase>>

AuthorizeCleanup ==
    /\ phase = "held"
    /\ provider # {}
    /\ cleanupApproved
    /\ observed = provider
    /\ uncertain = {}
    /\ cleanupAuthorized' = TRUE
    /\ phase' = "cleanup"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, terminal>>

RetireExactResources ==
    /\ phase = "cleanup"
    /\ cleanupAuthorized
    /\ observed = provider
    /\ provider' = {}
    /\ observed' = {}
    /\ configured' = {}
    /\ terminal' = TRUE
    /\ phase' = "retired"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, uncertain, pkiReady, cleanupApproved,
                   cleanupAuthorized>>

Complete ==
    /\ phase = "configured"
    /\ provider = Groups /\ observed = Groups /\ configured = Groups
    /\ admitted /\ intentBound /\ nonceUsed /\ pkiReady
    /\ terminal' = TRUE
    /\ phase' = "complete"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, cleanupAuthorized>>

ControllerCrash ==
    /\ phase \in {"ready-to-apply", "creating", "created", "configuring", "configured"}
    /\ phase' = "held"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, cleanupAuthorized, terminal>>

RecoverSameIntent ==
    /\ phase = "held"
    /\ intentBound /\ admitted /\ nonceUsed
    /\ uncertain = {}
    /\ observed = provider
    /\ phase' = "recovering"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, cleanupAuthorized, terminal>>

ResumeAfterReadback ==
    /\ phase = "recovering"
    /\ observed = provider
    /\ phase' = IF provider = Groups THEN
                   IF configured = Groups THEN "configured" ELSE "created"
                 ELSE "creating"
    /\ UNCHANGED <<planValid, capacityBound, nonceValid, nonceUsed,
                   accountValid, backendValid, sshReady, admitted,
                   intentBound, provider, observed, configured, uncertain,
                   pkiReady, cleanupApproved, cleanupAuthorized, terminal>>

Next == AdmitIntent \/ RejectOrExpire \/ ProviderAccountPreflight \/ PrepareSSH
        \/ SupplyReviewedPlan \/ BindCapacityIntent
        \/ \E createGroup \in Groups: CreateGroup(createGroup)
        \/ \E lostGroup \in Groups: LoseCreateResponse(lostGroup)
        \/ ReadProviderState \/ PreparePKI
        \/ \E configuredGroup \in Groups: ConfigureGroup(configuredGroup)
        \/ ApproveExactCleanup \/ AuthorizeCleanup \/ RetireExactResources
        \/ Complete \/ ControllerCrash \/ RecoverSameIntent \/ ResumeAfterReadback

DoneStutter == phase \in {"complete", "retired"} /\ UNCHANGED vars
Spec == Init /\ [][Next \/ DoneStutter]_vars

TypeOK ==
    /\ phase \in {"inputs", "ready-to-apply", "creating", "created",
                   "configuring", "configured", "held", "recovering",
                   "cleanup", "complete", "retired"}
    /\ provider \subseteq Groups
    /\ observed \subseteq Groups
    /\ configured \subseteq Groups
    /\ uncertain \subseteq Groups
ProviderEffectsRequireAdmission == provider # {} => (admitted /\ intentBound /\ nonceUsed)
ObservedOnlyAfterCreate == observed \subseteq provider
ConfigurationNeedsCreatedGroup == configured \subseteq observed
NoCleanupWithUnknownIdentity == cleanupAuthorized => (observed = provider /\ uncertain = {})
TerminalSuccessIsComplete == terminal /\ phase = "complete" =>
    (provider = Groups /\ observed = Groups /\ configured = Groups /\ pkiReady)
TerminalRetirementIsEmpty == terminal /\ phase = "retired" =>
    (provider = {} /\ observed = {} /\ configured = {})
NoEffectsWhileHeld == phase = "held" => ~cleanupAuthorized

=============================================================================
