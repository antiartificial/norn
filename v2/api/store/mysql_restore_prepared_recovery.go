package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// recoverExpiredPreparedMySQLRestores releases only reservations that never
// crossed the one-way SQL boundary. Lock the operation first, as Begin does,
// then lock and recheck the intent. A concurrent Begin either wins and changes
// it to executing, or observes the failed operation and cannot start SQL.
func recoverExpiredPreparedMySQLRestores(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT o.id FROM operations o
		JOIN mysql_restore_intents i ON i.operation_id=o.id
		WHERE o.kind='database.mysql-restore' AND o.status='running'
		  AND (o.locked_until IS NULL OR o.locked_until<clock_timestamp())
		  AND i.state='prepared'
		FOR UPDATE OF o SKIP LOCKED`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM mysql_restore_intents WHERE operation_id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
			return err
		}
		if state != "prepared" {
			continue
		}
		result, err := tx.Exec(ctx, `UPDATE operations SET status='failed',
			message='MySQL restore preparation expired before SQL; target reservation released',
			last_error='MySQL restore executor lease expired before execution',
			metadata=metadata || '{"mysqlRestoreState":"abandoned-before-execution"}'::jsonb,
			locked_by='', locked_until=NULL, updated_at=clock_timestamp(), finished_at=clock_timestamp()
			WHERE id=$1 AND kind='database.mysql-restore' AND status='running'
			  AND (locked_until IS NULL OR locked_until<clock_timestamp())`, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("expired prepared MySQL restore operation changed while recovering: %w", ErrMySQLRestoreFence)
		}
		result, err = tx.Exec(ctx, `DELETE FROM mysql_restore_maintenance_fences WHERE operation_id=$1`, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return fmt.Errorf("expired prepared MySQL restore maintenance fence changed while recovering: %w", ErrMySQLRestoreFence)
		}
		result, err = tx.Exec(ctx, `DELETE FROM mysql_restore_intents WHERE operation_id=$1 AND state='prepared'`, id)
		if err != nil || result.RowsAffected() != 1 {
			return fmt.Errorf("expired prepared MySQL restore reservation changed while recovering: %w", ErrMySQLRestoreFence)
		}
	}
	return nil
}
