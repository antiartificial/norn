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
	{"operation_request_identities", ClassHotEvidence, nil, "replay identities until an explicit expiry contract"},
	{"operation_acceptance_intents", ClassHotEvidence, []string{"request_canonical_bytes", "canonical_bytes"}, "original signed bytes; archived byte-exact inside saga bundles and terminal Fleet GitHub receipt bundles; hot replay identity remains authoritative"},
	{"operation_effects", ClassHotEvidence, []string{"launch_payload"}, "unresolved effects and retry-safety evidence"},
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
}
