package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type MutationAuditEvent struct {
	ID               string                 `json:"id"`
	RequestID        string                 `json:"requestId,omitempty"`
	PrincipalSubject string                 `json:"principalSubject"`
	TokenID          string                 `json:"tokenId,omitempty"`
	DeviceID         string                 `json:"deviceId,omitempty"`
	Scopes           []string               `json:"scopes,omitempty"`
	Method           string                 `json:"method"`
	Path             string                 `json:"path"`
	ClientIP         string                 `json:"clientIp,omitempty"`
	UserAgent        string                 `json:"userAgent,omitempty"`
	Status           int                    `json:"status"`
	Outcome          string                 `json:"outcome"`
	StartedAt        time.Time              `json:"startedAt"`
	FinishedAt       *time.Time             `json:"finishedAt,omitempty"`
	DurationMs       int64                  `json:"durationMs"`
	RecordDigest     string                 `json:"-"`
	KeyID            string                 `json:"keyId,omitempty"`
	Integrity        string                 `json:"integrity,omitempty"`
	Incident         *MutationAuditIncident `json:"incident,omitempty"`
}

type MutationAuditIncident struct {
	ID             string    `json:"id"`
	AuditEventID   string    `json:"auditEventId"`
	ReasonCode     string    `json:"reasonCode"`
	Explanation    string    `json:"explanation"`
	AcknowledgedBy string    `json:"acknowledgedBy"`
	AcknowledgedAt time.Time `json:"acknowledgedAt"`
	KeyID          string    `json:"keyId,omitempty"`
	RecordDigest   string    `json:"-"`
	Integrity      string    `json:"integrity,omitempty"`
}

func (db *DB) ReserveMutationAudit(ctx context.Context, event *MutationAuditEvent) error {
	scopes, err := json.Marshal(event.Scopes)
	if err != nil {
		return err
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO mutation_audit_events
		(id,request_id,principal_subject,token_id,device_id,scopes,method,path,client_ip,user_agent,status,outcome,started_at,key_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,0,'started',$11,$12)
	`, event.ID, event.RequestID, event.PrincipalSubject, event.TokenID, event.DeviceID, scopes,
		event.Method, event.Path, event.ClientIP, event.UserAgent, event.StartedAt, event.KeyID)
	return err
}

func (db *DB) FinishMutationAudit(ctx context.Context, id, path string, status int, outcome string, finishedAt time.Time, durationMs int64, digest string) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE mutation_audit_events
		SET path=$2,status=$3,outcome=$4,finished_at=$5,duration_ms=$6,record_digest=$7
		WHERE id=$1 AND outcome='started'
	`, id, path, status, outcome, finishedAt, durationMs, digest)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

const mutationAuditColumns = `id,request_id,principal_subject,token_id,device_id,scopes,method,path,client_ip,user_agent,
	status,outcome,started_at,finished_at,duration_ms,record_digest,key_id`

func scanMutationAudit(row pgx.Row) (*MutationAuditEvent, error) {
	var event MutationAuditEvent
	var scopes []byte
	if err := row.Scan(&event.ID, &event.RequestID, &event.PrincipalSubject, &event.TokenID, &event.DeviceID,
		&scopes, &event.Method, &event.Path, &event.ClientIP, &event.UserAgent, &event.Status, &event.Outcome,
		&event.StartedAt, &event.FinishedAt, &event.DurationMs, &event.RecordDigest, &event.KeyID); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(scopes, &event.Scopes)
	return &event, nil
}

func (db *DB) ListMutationAudits(ctx context.Context, limit int) ([]MutationAuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Pool.Query(ctx, `SELECT `+mutationAuditColumns+` FROM mutation_audit_events ORDER BY started_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []MutationAuditEvent{}
	for rows.Next() {
		event, err := scanMutationAudit(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, *event)
	}
	return events, rows.Err()
}

func (db *DB) GetMutationAudit(ctx context.Context, id string) (*MutationAuditEvent, error) {
	return scanMutationAudit(db.Pool.QueryRow(ctx, `SELECT `+mutationAuditColumns+` FROM mutation_audit_events WHERE id=$1`, id))
}

func (db *DB) InsertMutationAuditIncident(ctx context.Context, incident *MutationAuditIncident) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO mutation_audit_incidents
		(id,audit_event_id,reason_code,explanation,acknowledged_by,acknowledged_at,key_id,record_digest)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
	`, incident.ID, incident.AuditEventID, incident.ReasonCode, incident.Explanation,
		incident.AcknowledgedBy, incident.AcknowledgedAt, incident.KeyID, incident.RecordDigest)
	return err
}

func (db *DB) ListMutationAuditIncidents(ctx context.Context, eventIDs []string) (map[string]MutationAuditIncident, error) {
	incidents := map[string]MutationAuditIncident{}
	if len(eventIDs) == 0 {
		return incidents, nil
	}
	rows, err := db.Pool.Query(ctx, `
		SELECT id,audit_event_id,reason_code,explanation,acknowledged_by,acknowledged_at,key_id,record_digest
		FROM mutation_audit_incidents WHERE audit_event_id = ANY($1::text[])
	`, eventIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var incident MutationAuditIncident
		if err := rows.Scan(&incident.ID, &incident.AuditEventID, &incident.ReasonCode, &incident.Explanation,
			&incident.AcknowledgedBy, &incident.AcknowledgedAt, &incident.KeyID, &incident.RecordDigest); err != nil {
			return nil, err
		}
		incidents[incident.AuditEventID] = incident
	}
	return incidents, rows.Err()
}

func (db *DB) CountStaleMutationAudits(ctx context.Context, before time.Time) (int, error) {
	var count int
	err := db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM mutation_audit_events WHERE outcome='started' AND started_at<$1
	`, before).Scan(&count)
	return count, err
}

func (db *DB) PruneMutationAudits(ctx context.Context, before time.Time) (int64, error) {
	result, err := db.Pool.Exec(ctx, `
		DELETE FROM mutation_audit_events WHERE finished_at IS NOT NULL AND finished_at<$1
	`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}
