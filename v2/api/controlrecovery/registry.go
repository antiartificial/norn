// Package controlrecovery provides offline, read-only inspection of Norn's
// PostgreSQL control schema. Inspection output is deliberately redacted and is
// not a recovery artifact.
package controlrecovery

// Column classifies one physical control-table column. Columns absent from the
// registry fail inspection. Included columns are emitted; excluded columns are
// intentionally omitted rather than passed through a generic redactor.
type Column struct {
	Name    string
	Include bool
}

// Table classifies one physical control table and its deterministic row order.
type Table struct {
	Name    string
	OrderBy []string
	Columns []Column
}

func include(names ...string) []Column {
	columns := make([]Column, 0, len(names))
	for _, name := range names {
		columns = append(columns, Column{Name: name, Include: true})
	}
	return columns
}

func classified(included []Column, excluded ...string) []Column {
	columns := append([]Column(nil), included...)
	for _, name := range excluded {
		columns = append(columns, Column{Name: name})
	}
	return columns
}

// InspectionRegistry returns a fresh copy of the explicit migration-2 control
// schema registry. The registry includes schema metadata created by the
// migration runner as well as every table in migrations 1 and 2.
func InspectionRegistry() []Table {
	tables := []Table{
		{Name: "norn_schema_migrations", OrderBy: []string{"version"}, Columns: include("version", "name", "checksum", "minimum_reader_version", "minimum_writer_version", "applied_at")},
		{Name: "norn_schema_compatibility", OrderBy: []string{"singleton"}, Columns: include("singleton", "format_version", "current_migration_version", "minimum_reader_version", "minimum_writer_version", "updated_at")},
		{Name: "saga_events", OrderBy: []string{"id"}, Columns: classified(include("id", "saga_id", "timestamp", "source", "app", "category", "action"), "message", "metadata")},
		{Name: "control_events", OrderBy: []string{"id"}, Columns: classified(include("id", "timestamp", "type", "app_id"), "payload")},
		{Name: "deployments", OrderBy: []string{"id"}, Columns: classified(include("id", "app", "commit_sha", "image_tag", "environment", "saga_id", "status", "source_kind", "source_ref", "source_dirty", "started_at", "finished_at"), "source_changes")},
		{Name: "deployment_regions", OrderBy: []string{"deployment_id", "region"}, Columns: classified(include("deployment_id", "region", "nomad_region", "status", "desired_weight", "active_weight", "eval_id", "updated_at", "datacenters"), "last_error")},
		{Name: "deployment_steps", OrderBy: []string{"deployment_id", "step"}, Columns: classified(include("deployment_id", "app", "saga_id", "step", "kind", "status", "attempt", "started_at", "finished_at", "duration_ms"), "message", "metadata")},
		{Name: "cron_states", OrderBy: []string{"app", "process"}, Columns: include("app", "process", "paused", "schedule", "updated_at")},
		{Name: "func_executions", OrderBy: []string{"id"}, Columns: include("id", "app", "process", "status", "exit_code", "started_at", "finished_at", "duration_ms")},
		{Name: "beacon_events", OrderBy: []string{"id"}, Columns: classified(include("id", "source", "app", "environment", "type", "severity", "dedupe_key", "occurred_at", "acknowledged_at", "acknowledged_by", "snoozed_until"), "title", "body", "acknowledgement_note", "metadata")},
		{Name: "operations", OrderBy: []string{"id"}, Columns: classified(include("id", "kind", "app", "saga_id", "ref", "status", "risk", "source", "attempts", "max_attempts", "locked_by", "lock_generation", "locked_until", "next_attempt_at", "started_at", "updated_at", "finished_at", "acceptance_required"), "message", "payload", "metadata", "last_error")},
		{Name: "fleet_runner_attempts", OrderBy: []string{"id"}, Columns: classified(include("id", "plan_id", "attempt", "runner_attempt_id", "commit_sha", "plan_sha256", "status", "current_phase", "root_attempt_id", "retry_of", "heartbeat_sequence", "heartbeat_timeout_seconds", "revision", "heartbeat_at", "heartbeat_expires_at", "started_at", "updated_at", "finished_at"), "workflow_url", "message")},
		{Name: "fleet_github_dispatches", OrderBy: []string{"plan_id"}, Columns: classified(include("plan_id", "plan_run_id", "plan_sha256", "approved_head_sha", "fleet_environment", "allow_destructive", "dispatch_nonce_sha256", "run_id", "created_at", "updated_at"), "dispatch_nonce", "workflow_url")},
		{Name: "webhook_deliveries", OrderBy: []string{"id"}, Columns: classified(include("id", "provider", "event", "delivery_id", "repository", "ref", "branch", "app", "saga_id", "status", "received_at", "updated_at"), "reason", "remote_addr", "user_agent", "payload", "metadata")},
		{Name: "notification_channels", OrderBy: []string{"id"}, Columns: classified(include("id", "provider", "name", "severities", "created_at"), "url", "token", "user_key")},
		{Name: "access_grants", OrderBy: []string{"id"}, Columns: classified(include("id", "created_by", "created_at", "expires_at"), "ip", "note")},
		{Name: "access_devices", OrderBy: []string{"id"}, Columns: include("id", "name", "platform", "model", "app_version", "public_key", "created_at", "last_seen_at", "revoked_at")},
		{Name: "access_tokens", OrderBy: []string{"jti"}, Columns: include("jti", "device_id", "subject", "scopes", "issued_at", "expires_at", "revoked_at", "rotated_from")},
		{Name: "github_actions_assertion_uses", OrderBy: []string{"issuer", "jti"}, Columns: include("issuer", "jti", "expires_at", "used_at")},
		{Name: "access_enrollments", OrderBy: []string{"id"}, Columns: classified(include("id", "requested_scopes", "approved_scopes", "verifier_attempts", "status", "device_id", "created_at", "expires_at", "approved_at", "exchanged_at"), "code_hash", "verifier_hash", "device_name", "platform", "model", "app_version", "public_key", "source_hash")},
		{Name: "step_up_challenges", OrderBy: []string{"id"}, Columns: classified(include("id", "device_id", "purpose", "status", "created_at", "expires_at", "verified_at", "consumed_at"), "token_jti", "resource", "nonce_hash")},
		{Name: "exec_sessions", OrderBy: []string{"id"}, Columns: classified(include("id", "device_id", "token_jti", "challenge_id", "app_id", "allocation_id", "task", "command_digest", "terminal", "columns", "rows", "status", "created_at", "expires_at", "connected_at", "finished_at", "exit_code", "error_code", "owner_id", "owner_lease_until"), "command", "remote_addr", "user_agent", "owner_token")},
		{Name: "mutation_audit_events", OrderBy: []string{"id"}, Columns: classified(include("id", "request_id", "principal_subject", "token_id", "device_id", "scopes", "method", "path", "status", "outcome", "started_at", "finished_at", "duration_ms", "record_digest", "key_id"), "client_ip", "user_agent")},
		{Name: "mutation_audit_incidents", OrderBy: []string{"id"}, Columns: classified(include("id", "audit_event_id", "reason_code", "acknowledged_by", "acknowledged_at", "key_id", "record_digest"), "explanation")},
		{Name: "recovery_drills", OrderBy: []string{"id"}, Columns: classified(include("id", "kind", "target", "status", "initiated_by", "started_at", "finished_at"), "evidence")},
		{Name: "access_observation_buckets", OrderBy: []string{"app", "process", "endpoint", "source", "bucket_start"}, Columns: include("app", "process", "endpoint", "source", "bucket_start", "requests", "successes", "client_errors", "server_errors", "first_seen", "last_seen")},
		{Name: "control_plane_identity", OrderBy: []string{"singleton"}, Columns: include("singleton", "authority", "created_at")},
		{Name: "operation_request_identities", OrderBy: []string{"id"}, Columns: classified(include("id", "authority", "actor_issuer", "actor_subject", "kind", "resource", "fingerprint_version", "fingerprint_digest", "operation_id", "created_at"), "request_key")},
		{Name: "operation_acceptance_intents", OrderBy: []string{"id"}, Columns: classified(include("id", "schema_version", "request_identity_id", "operation_id", "deployment_id", "accepted_at", "request_receipt_id", "request_id", "credential_id", "device_id", "source", "scopes", "fingerprint_version", "fingerprint_digest", "canonical_digest", "signing_algorithm", "signing_key_id"), "request_canonical_bytes", "canonical_bytes", "signature")},
		{Name: "operation_effects", OrderBy: []string{"id"}, Columns: classified(include("id", "generation", "authority", "resource", "operation_id", "stage", "supervisor", "lifecycle", "outcome", "exit_code", "resolution_decision", "created_at", "launched_at", "completed_at", "resolved_at", "updated_at"), "claim_owner", "claim_generation", "input_digest", "launch_payload", "supervisor_execution_id", "runtime_instance_id", "result_digest", "result_reference", "evidence_source", "evidence_reference", "evidence_observed_at")},
		// Checkpoint outputs name source paths, changed files and image
		// references; inspection keeps only identity and the integrity digest.
		{Name: "operation_checkpoints", OrderBy: []string{"operation_id", "stage"}, Columns: classified(include("operation_id", "stage", "claim_generation", "outputs_digest", "created_at"), "outputs")},
		// Catalog documents carry credential/TLS references and provider
		// topology; inspection keeps only revision lineage and digests.
		{Name: "database_catalog_revisions", OrderBy: []string{"revision"}, Columns: classified(include("revision", "previous_revision", "catalog_digest", "activated_by", "activated_at"), "catalog")},
		{Name: "database_catalog_retirements", OrderBy: []string{"kind", "id"}, Columns: include("kind", "id", "retired_revision")},
		{Name: "evidence_archive_intents", OrderBy: []string{"id"}, Columns: include("id", "subject_kind", "subject_id", "app", "operation_id", "sequence", "state", "event_ids", "event_count", "cutoff_timestamp",
			"object_key", "object_sha256", "object_bytes", "attempts", "last_error", "pruned_events", "created_at", "updated_at", "verified_at", "pruned_at")},
		{Name: "evidence_reserve", OrderBy: []string{"singleton"}, Columns: include("singleton", "enabled", "max_pending", "max_pending_age_seconds", "archive_exhausted", "archive_detail",
			"archive_observed_at", "updated_at")},
	}

	result := make([]Table, len(tables))
	for index, table := range tables {
		result[index] = Table{
			Name:    table.Name,
			OrderBy: append([]string(nil), table.OrderBy...),
			Columns: append([]Column(nil), table.Columns...),
		}
	}
	return result
}
