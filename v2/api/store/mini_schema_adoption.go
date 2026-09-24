package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// This fingerprints the PG17 Mini schema after its schema-only dump is
// restored into the supported PG16 target. The canonical form omits catalog
// OIDs while retaining the complete table set, columns, defaults, nullability,
// constraints, and full index definitions including validity.
const miniLegacyStructuralFingerprint = "0cfd4a1141491cd72bfbe464760653cbe39a04f76002829e84a637129029dae1"

var miniLegacyTableNames = []string{"access_devices", "access_enrollments", "access_grants", "access_observation_buckets", "access_tokens", "beacon_events", "control_events", "cron_states", "deployment_regions", "deployment_steps", "deployments", "exec_sessions", "external_deployment_admission_checkpoints", "external_deployment_admissions", "external_deployment_nonces", "fleet_github_dispatches", "fleet_runner_attempts", "fleet_runner_checkpoint_refs", "func_executions", "github_actions_assertion_uses", "mutation_audit_events", "mutation_audit_incidents", "notification_channels", "operations", "recovery_drills", "saga_events", "step_up_challenges", "webhook_deliveries"}

type MiniSchemaAdoptionError struct{ Reason string }

func (e *MiniSchemaAdoptionError) Error() string {
	return "unversioned Mini schema adoption refused: " + e.Reason
}

func adoptMiniControlSchema(ctx context.Context, tx pgx.Tx) error {
	return adoptMiniControlSchemaFingerprint(ctx, tx, miniLegacyStructuralFingerprint, miniLegacyTableNames)
}

func adoptMiniControlSchemaFingerprint(ctx context.Context, tx pgx.Tx, expectedFingerprint string, expectedTables []string) error {
	var markerCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') AND c.relname = ANY($1)`, []string{"external_deployment_nonces", "external_deployment_admissions", "external_deployment_admission_checkpoints", "fleet_runner_checkpoint_refs"}).Scan(&markerCount); err != nil {
		return err
	}
	if markerCount == 0 {
		return nil
	}
	if markerCount != 4 {
		return &MiniSchemaAdoptionError{Reason: "pilot marker tables are incomplete"}
	}
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') ORDER BY c.relname`)
	if err != nil {
		return err
	}
	var actualTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		actualTables = append(actualTables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if strings.Join(actualTables, "\x00") != strings.Join(expectedTables, "\x00") {
		return &MiniSchemaAdoptionError{Reason: "control tables do not match the pinned Mini table set"}
	}
	if err := lockMiniDispatchTable(ctx, tx); err != nil {
		return err
	}

	fingerprint, err := miniSchemaStructuralFingerprint(ctx, tx)
	if err != nil {
		return err
	}
	if fingerprint != expectedFingerprint {
		return &MiniSchemaAdoptionError{Reason: "control schema does not match the pinned Mini structural fingerprint"}
	}

	var dispatchRows, inFlightDispatches int64
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE run_id=0) FROM fleet_github_dispatches`).Scan(&dispatchRows, &inFlightDispatches); err != nil {
		return err
	}
	if dispatchRows != 0 {
		return &MiniSchemaAdoptionError{Reason: fmt.Sprintf("fleet dispatch adoption requires zero rows (rows=%d in_flight=%d)", dispatchRows, inFlightDispatches)}
	}

	_, err = tx.Exec(ctx, `
		ALTER TABLE fleet_runner_attempts ADD COLUMN heartbeat_expires_at TIMESTAMPTZ;
		UPDATE fleet_runner_attempts SET heartbeat_expires_at = heartbeat_at + make_interval(secs => heartbeat_timeout_seconds);
		ALTER TABLE fleet_runner_attempts ALTER COLUMN heartbeat_expires_at SET NOT NULL;
		ALTER TABLE fleet_runner_attempts ADD COLUMN message TEXT NOT NULL DEFAULT '';
		UPDATE fleet_runner_attempts SET message = last_error WHERE last_error <> '';
		ALTER TABLE fleet_github_dispatches ADD COLUMN dispatch_nonce TEXT NOT NULL DEFAULT '';
	`)
	return err
}

func lockMiniDispatchTable(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `LOCK TABLE fleet_github_dispatches IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

func miniSchemaStructuralFingerprint(ctx context.Context, tx pgx.Tx) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT kind,table_name,column_name,type_name,not_null,definition FROM (
			SELECT 'C'::text kind,c.relname table_name,a.attname column_name,
				pg_catalog.format_type(a.atttypid,a.atttypmod) type_name,a.attnotnull::text not_null,
				COALESCE(pg_get_expr(d.adbin,d.adrelid),'') definition
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid
			LEFT JOIN pg_catalog.pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
			WHERE n.nspname=current_schema() AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped
			UNION ALL
			SELECT 'K',c.relname,'',x.contype::text,'',pg_get_constraintdef(x.oid,true)
			FROM pg_catalog.pg_constraint x JOIN pg_catalog.pg_class c ON c.oid=x.conrelid
			JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relkind IN ('r','p')
			UNION ALL
			SELECT 'I',c.relname,'',concat_ws(',',i.indisunique::text,i.indisprimary::text,i.indisvalid::text),'',
				pg_get_indexdef(i.indexrelid)
			FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c ON c.oid=i.indrelid
			JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relkind IN ('r','p')
		) signature ORDER BY kind,table_name,column_name,type_name,not_null,definition`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	hash := sha256.New()
	for rows.Next() {
		var fields [6]string
		if err := rows.Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &fields[5]); err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%s|%s|%s|%s|%s|%s\n", fields[0], fields[1], fields[2], fields[3], fields[4], fields[5])
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
