package retention

// Retention classes for control-store tables (ADR 0001, retention handoff).
const (
	// ClassArchived: completed evidence is archived, verified and pruned under
	// holds; every read path is archive-aware.
	ClassArchived = "archived-and-pruned"
	// ClassHotEvidence: bulky or authoritative evidence that stays hot today.
	// Archival is required for bounded control storage and not implemented.
	ClassHotEvidence = "hot-evidence-archival-pending"
	// ClassExpiring: replay/validity state deleted by an explicit expiry rule
	// once it can no longer authorize or be replayed.
	ClassExpiring = "expiring-by-validity"
	// ClassAgePolicy: an existing age-only deletion rule, not yet the
	// archive-backed retention policy.
	ClassAgePolicy = "existing-age-deletion"
	// ClassCurrentState: authoritative current state or configuration, not
	// history; kept hot, never archived (credentials stay out of bundles).
	ClassCurrentState = "current-state"
)

// PayloadRetention describes one control table's retention.
type PayloadRetention struct {
	Table string
	Class string
	// Bulky names the columns that dominate growth.
	Bulky []string
	// Holds summarizes what must stay hot before any pruning.
	Holds string
}

// PayloadInventory classifies every control-store table. It is checked
// against the recovery registry, so a new table cannot appear without a
// retention decision.
var PayloadInventory = []PayloadRetention{
	{"saga_events", ClassArchived, []string{"metadata", "message"}, "operation/deployment/effect holds, rollback candidates, minimum age, reader floor and connected readers"},
	{"operations", ClassHotEvidence, []string{"payload", "metadata", "last_error"}, "active, indeterminate and manual-recovery work; archived copy exists inside saga bundles and terminal Fleet GitHub receipt bundles"},
	{"operation_request_identities", ClassHotEvidence, nil, "versioned replay expiry retains the identity/fingerprint namespace tombstone; a matching control database backup is required for operation-receipt index recovery"},
	{"operation_acceptance_intents", ClassHotEvidence, []string{"request_canonical_bytes", "canonical_bytes"}, "archive-verified expired terminal Fleet GitHub receipts can retire the hot signed payload; other acceptance kinds remain hot"},
	{"signed_acceptance_byte_reservations", ClassHotEvidence, nil, "logical bytes are released atomically only with eligible archive-backed Fleet GitHub acceptance retirement; other reservations remain hot"},
	{"release_attestation_byte_reservations", ClassHotEvidence, nil, "logical release attestation bytes remain reserved until their hot operation is retired through a qualified archive reader"},
	{"retired_operation_acceptances", ClassCurrentState, nil, "permanent operation, replay-identity, audit-receipt and archive linkage after hot payload retirement"},
	{"operation_effects", ClassHotEvidence, []string{"launch_payload"}, "unresolved effects and retry-safety evidence"},
	{"restart_effect_sources", ClassHotEvidence, nil, "per-source restart attempt and acknowledgement evidence"},
	{"function_invocation_effect_attempts", ClassHotEvidence, nil, "variable and job pre-call fences; retained while an invocation can require recovery or manual review"},
	{"snapshot_publication_intents", ClassHotEvidence, []string{"target", "namespace", "filename"}, "snapshot publication and retry-safety intent; retained until object/copy recovery is qualified"},
	{"snapshot_export_intents", ClassHotEvidence, nil, "unresolved export reservations and publication recovery remain available until their terminal evidence is qualified"},
	{"private_invocation_material", ClassHotEvidence, nil, "encrypted invocation material remains available for accepted work and key-retirement preflight; never enters archive bundles"},
	{"function_invocation_cleanup_intents", ClassHotEvidence, nil, "variable cleanup and remote job teardown intents remain until completion or manual recovery"},
	{"mysql_runtime_launch_reservations", ClassHotEvidence, nil, "launch reservations remain through writer fencing and recovery reconciliation"},
	{"mysql_restore_recovery_intents", ClassHotEvidence, nil, "unresolved restore recovery intent and source binding remain until terminal proof"},
	{"mysql_source_snapshot_reconciliations", ClassHotEvidence, nil, "source snapshot reconciliation history proves ambiguous attempts before retry or release"},
	{"mysql_source_snapshot_intents", ClassHotEvidence, nil, "source snapshot and quiescence intent remain until durable artifact and unlock proof"},
	{"mysql_restore_intents", ClassHotEvidence, nil, "restore intent and target identity remain through copy, unlock, and recovery"},
	{"database_cutover_journals", ClassHotEvidence, []string{"intent", "receipts"}, "source and target identity, phase and receipts remain through cutover recovery and source-retirement proof"},
	{"mysql_restore_maintenance_fences", ClassHotEvidence, nil, "maintenance fence ownership and release proof remain while restore may be retried"},
	{"mysql_restore_runtime_locks", ClassHotEvidence, nil, "runtime lock state remains until restore releases all protected writers"},
	{"operation_checkpoints", ClassHotEvidence, []string{"outputs"}, "retry-safety checkpoints of unresolved operations"},
	{"deployments", ClassHotEvidence, []string{"source_changes"}, "current and rollback candidates, routing state"},
	{"deployment_regions", ClassHotEvidence, nil, "regional routing of current deployments"},
	{"deployment_steps", ClassHotEvidence, []string{"message", "metadata"}, "unresolved steps of active deployments"},
	{"control_events", ClassHotEvidence, []string{"payload"}, "bounded replay pruning uses an explicit expired-cursor resync contract and durable compaction watermark"},
	{"webhook_deliveries", ClassHotEvidence, []string{"payload"}, "pending and replayable deliveries"},
	{"func_executions", ClassHotEvidence, nil, "unresolved invocations (output is not stored)"},
	{"fleet_runner_attempts", ClassHotEvidence, nil, "root/retry lineage and active attempts"},
	{"fleet_github_dispatches", ClassHotEvidence, nil, "approval/nonce correlation and recoverable run identity"},
	{"exec_sessions", ClassHotEvidence, nil, "active leases and unresolved completion"},
	{"mutation_audit_incidents", ClassHotEvidence, nil, "unresolved incidents"},
	{"recovery_drills", ClassHotEvidence, nil, "latest qualifying proof used by production readiness"},
	{"mutation_audit_events", ClassAgePolicy, []string{"record_digest"}, "existing 365-day deletion keeps acceptance-linked receipts; archive-backed policy pending"},
	{"beacon_events", ClassAgePolicy, []string{"body", "metadata"}, "existing age-only deletion ignores open/acknowledged state; replacement pending"},
	{"access_observation_buckets", ClassAgePolicy, nil, "audit decision readers before treating as expiring metrics"},
	{"access_grants", ClassExpiring, nil, "deleted once expired"},
	{"access_enrollments", ClassExpiring, nil, "deleted 30 days after expiry"},
	{"step_up_challenges", ClassExpiring, nil, "deleted 30 days after expiry"},
	{"github_actions_assertion_uses", ClassExpiring, nil, "deleted once the assertion can no longer be replayed"},
	{"access_devices", ClassCurrentState, nil, "revocation and credential ancestry"},
	{"access_tokens", ClassCurrentState, nil, "revocation; token lineage participates in operation identity"},
	{"cron_states", ClassCurrentState, nil, "current schedule state"},
	{"notification_channels", ClassCurrentState, nil, "configuration with credentials; never archived"},
	{"control_plane_identity", ClassCurrentState, nil, "authority identity"},
	{"norn_schema_migrations", ClassCurrentState, nil, "schema ledger"},
	{"norn_schema_compatibility", ClassCurrentState, nil, "compatibility floor"},
	{"database_catalog_revisions", ClassCurrentState, []string{"catalog"}, "catalog lineage; references credentials, never archived"},
	{"database_catalog_retirements", ClassCurrentState, nil, "retired identities"},
	{"app_desired_replicas", ClassCurrentState, nil, "authoritative regional operator replica intent consumed by deployment and rollback translation"},
	{"evidence_archive_intents", ClassCurrentState, []string{"event_ids"}, "archive index; rebuildable from the archive (archive-reindex)"},
	{"evidence_reserve", ClassCurrentState, nil, "admission policy and capacity observation"},
	{"control_event_retention", ClassCurrentState, nil, "singleton replay-compaction watermark; retained so expired cursors can require resynchronization"},
	{"runtime_mutation_fence", ClassCurrentState, nil, "singleton writer mutation epoch and owner; retained across restarts to fence stale restore workers"},
}
