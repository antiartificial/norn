package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrTooManyActiveExecSessions = errors.New("too many active exec sessions")

type StepUpChallenge struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"deviceId"`
	TokenJTI   string     `json:"tokenId"`
	Purpose    string     `json:"purpose"`
	Resource   string     `json:"resource"`
	NonceHash  string     `json:"-"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	ConsumedAt *time.Time `json:"consumedAt,omitempty"`
}

type ExecSession struct {
	ID            string     `json:"id"`
	DeviceID      string     `json:"deviceId"`
	TokenJTI      string     `json:"tokenId"`
	ChallengeID   string     `json:"challengeId"`
	AppID         string     `json:"appId"`
	AllocationID  string     `json:"allocationId"`
	Task          string     `json:"task"`
	Command       []string   `json:"-"`
	CommandDigest string     `json:"commandDigest"`
	Terminal      bool       `json:"terminal"`
	Columns       int        `json:"columns"`
	Rows          int        `json:"rows"`
	Status        string     `json:"status"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	ConnectedAt   *time.Time `json:"connectedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	ExitCode      *int       `json:"exitCode,omitempty"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	RemoteAddr    string     `json:"remoteAddress,omitempty"`
	UserAgent     string     `json:"userAgent,omitempty"`
}

func (db *DB) ActiveAccessDevice(ctx context.Context, id string) (*AccessDevice, error) {
	var device AccessDevice
	err := db.Pool.QueryRow(ctx, `
		SELECT id,name,platform,model,app_version,public_key,created_at,last_seen_at,revoked_at
		FROM access_devices WHERE id=$1 AND revoked_at IS NULL
	`, id).Scan(&device.ID, &device.Name, &device.Platform, &device.Model, &device.AppVersion,
		&device.PublicKey, &device.CreatedAt, &device.LastSeenAt, &device.RevokedAt)
	return &device, err
}

func (db *DB) CreateStepUpChallenge(ctx context.Context, challenge *StepUpChallenge) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "norn.step-up."+challenge.DeviceID); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `DELETE FROM step_up_challenges WHERE expires_at<now()-interval '30 days'`)
	var recent int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM step_up_challenges
		WHERE device_id=$1 AND created_at>now()-interval '10 minutes'
	`, challenge.DeviceID).Scan(&recent); err != nil {
		return err
	}
	if recent >= 20 {
		return ErrRateLimited
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO step_up_challenges(id,device_id,token_jti,purpose,resource,nonce_hash,status,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,'pending',$7,$8)
	`, challenge.ID, challenge.DeviceID, challenge.TokenJTI, challenge.Purpose, challenge.Resource,
		challenge.NonceHash, challenge.CreatedAt, challenge.ExpiresAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanStepUpChallenge(row pgx.Row) (*StepUpChallenge, error) {
	var challenge StepUpChallenge
	err := row.Scan(&challenge.ID, &challenge.DeviceID, &challenge.TokenJTI, &challenge.Purpose,
		&challenge.Resource, &challenge.NonceHash, &challenge.Status, &challenge.CreatedAt,
		&challenge.ExpiresAt, &challenge.VerifiedAt, &challenge.ConsumedAt)
	return &challenge, err
}

func (db *DB) GetStepUpChallenge(ctx context.Context, id string) (*StepUpChallenge, error) {
	return scanStepUpChallenge(db.Pool.QueryRow(ctx, `
		SELECT id,device_id,token_jti,purpose,resource,nonce_hash,status,created_at,expires_at,verified_at,consumed_at
		FROM step_up_challenges WHERE id=$1
	`, id))
}

func (db *DB) VerifyStepUpChallenge(ctx context.Context, id, deviceID string) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE step_up_challenges SET status='verified',verified_at=now()
		WHERE id=$1 AND device_id=$2 AND status='pending' AND expires_at>now()
	`, id, deviceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (db *DB) CreateExecSession(ctx context.Context, session *ExecSession) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "norn.exec-session."+session.DeviceID); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `
		UPDATE exec_sessions SET status='expired',finished_at=now(),error_code='exec_session_expired'
		WHERE device_id=$1 AND status='pending' AND expires_at<=now()
	`, session.DeviceID)
	var active, recent int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status IN ('pending','running')),
		       count(*) FILTER (WHERE created_at>now()-interval '1 hour')
		FROM exec_sessions WHERE device_id=$1
	`, session.DeviceID).Scan(&active, &recent); err != nil {
		return err
	}
	if active >= 3 {
		return ErrTooManyActiveExecSessions
	}
	if recent >= 30 {
		return ErrRateLimited
	}
	result, err := tx.Exec(ctx, `
		UPDATE step_up_challenges SET status='consumed',consumed_at=now()
		WHERE id=$1 AND device_id=$2 AND purpose='exec' AND resource=$3
		  AND token_jti=$4 AND status='verified' AND expires_at>now()
	`, session.ChallengeID, session.DeviceID, session.AppID, session.TokenJTI)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	command, _ := json.Marshal(session.Command)
	_, err = tx.Exec(ctx, `
		INSERT INTO exec_sessions
		(id,device_id,token_jti,challenge_id,app_id,allocation_id,task,command,command_digest,terminal,columns,rows,status,created_at,expires_at,remote_addr,user_agent)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'pending',$13,$14,$15,$16)
	`, session.ID, session.DeviceID, session.TokenJTI, session.ChallengeID, session.AppID,
		session.AllocationID, session.Task, command, session.CommandDigest, session.Terminal, session.Columns, session.Rows,
		session.CreatedAt, session.ExpiresAt, session.RemoteAddr, session.UserAgent)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const execSessionColumns = `id,device_id,token_jti,challenge_id,app_id,allocation_id,task,command,command_digest,terminal,
	columns,rows,status,created_at,expires_at,connected_at,finished_at,exit_code,error_code,remote_addr,user_agent`

func scanExecSession(row pgx.Row) (*ExecSession, error) {
	var session ExecSession
	var command []byte
	err := row.Scan(&session.ID, &session.DeviceID, &session.TokenJTI, &session.ChallengeID,
		&session.AppID, &session.AllocationID, &session.Task, &command, &session.CommandDigest, &session.Terminal,
		&session.Columns, &session.Rows, &session.Status, &session.CreatedAt, &session.ExpiresAt,
		&session.ConnectedAt, &session.FinishedAt, &session.ExitCode, &session.ErrorCode,
		&session.RemoteAddr, &session.UserAgent)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(command, &session.Command)
	return &session, nil
}

func (db *DB) GetExecSession(ctx context.Context, id string) (*ExecSession, error) {
	_ = db.ExpireExecSessions(ctx)
	return scanExecSession(db.Pool.QueryRow(ctx, `SELECT `+execSessionColumns+` FROM exec_sessions WHERE id=$1`, id))
}

func (db *DB) ExpireExecSessions(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE exec_sessions SET status='expired',finished_at=now(),error_code='exec_session_expired'
		WHERE status='pending' AND expires_at<=now()
	`)
	return err
}

func (db *DB) ConnectExecSession(ctx context.Context, id string) error {
	result, err := db.Pool.Exec(ctx, `
		UPDATE exec_sessions SET status='running',connected_at=now(),command='[]'
		WHERE id=$1 AND status='pending' AND expires_at>now()
		  AND EXISTS (SELECT 1 FROM access_devices WHERE id=exec_sessions.device_id AND revoked_at IS NULL)
	`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func cancelActiveExecSessionsTx(ctx context.Context, tx pgx.Tx, column, value, errorCode string) ([]string, error) {
	query := `UPDATE exec_sessions SET status='canceled',finished_at=now(),error_code=$2
		WHERE ` + column + `=$1 AND status IN ('pending','running') RETURNING id`
	rows, err := tx.Query(ctx, query, value, errorCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (db *DB) FinishExecSession(ctx context.Context, id, status string, exitCode *int, errorCode string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE exec_sessions SET status=$2,finished_at=now(),exit_code=$3,error_code=$4
		WHERE id=$1 AND status IN ('pending','running')
	`, id, status, exitCode, errorCode)
	return err
}

func (db *DB) ListExecSessions(ctx context.Context, deviceID string, all bool) ([]ExecSession, error) {
	_ = db.ExpireExecSessions(ctx)
	query := `SELECT ` + execSessionColumns + ` FROM exec_sessions`
	args := []interface{}{}
	if !all {
		query += ` WHERE device_id=$1`
		args = append(args, deviceID)
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ExecSession{}
	for rows.Next() {
		session, err := scanExecSession(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *session)
	}
	return result, rows.Err()
}
