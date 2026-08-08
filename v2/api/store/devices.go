package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrRateLimited = errors.New("rate limited")
var ErrEnrollmentLocked = errors.New("enrollment locked")

type AccessDevice struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Platform   string        `json:"platform,omitempty"`
	Model      string        `json:"model,omitempty"`
	AppVersion string        `json:"appVersion,omitempty"`
	PublicKey  string        `json:"-"`
	CreatedAt  time.Time     `json:"createdAt"`
	LastSeenAt *time.Time    `json:"lastSeenAt,omitempty"`
	RevokedAt  *time.Time    `json:"revokedAt,omitempty"`
	Tokens     []AccessToken `json:"tokens"`
}

type AccessToken struct {
	JTI         string     `json:"id"`
	DeviceID    string     `json:"deviceId,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	Scopes      []string   `json:"scopes"`
	IssuedAt    time.Time  `json:"issuedAt"`
	ExpiresAt   time.Time  `json:"expiresAt"`
	RevokedAt   *time.Time `json:"revokedAt,omitempty"`
	RotatedFrom string     `json:"rotatedFrom,omitempty"`
}

type AccessEnrollment struct {
	ID               string     `json:"id"`
	CodeHash         string     `json:"-"`
	VerifierHash     string     `json:"-"`
	DeviceName       string     `json:"deviceName"`
	Platform         string     `json:"platform,omitempty"`
	Model            string     `json:"model,omitempty"`
	AppVersion       string     `json:"appVersion,omitempty"`
	PublicKey        string     `json:"-"`
	RequestedScopes  []string   `json:"requestedScopes"`
	ApprovedScopes   []string   `json:"approvedScopes,omitempty"`
	SourceHash       string     `json:"-"`
	VerifierAttempts int        `json:"-"`
	Status           string     `json:"status"`
	DeviceID         string     `json:"deviceId,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	ApprovedAt       *time.Time `json:"approvedAt,omitempty"`
	ExchangedAt      *time.Time `json:"exchangedAt,omitempty"`
}

func (db *DB) CreateAccessEnrollment(ctx context.Context, enrollment *AccessEnrollment) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('norn.access.enrollment'))`); err != nil {
		return err
	}
	_, _ = tx.Exec(ctx, `
		UPDATE access_enrollments SET status='expired'
		WHERE status IN ('pending','approved') AND expires_at<=now();
		DELETE FROM access_enrollments WHERE expires_at<now()-interval '30 days';
	`)
	var recentGlobal, recentSource int
	if err := tx.QueryRow(ctx, `
		SELECT count(*),count(*) FILTER (WHERE source_hash=$1)
		FROM access_enrollments WHERE created_at>now()-interval '10 minutes'
	`, enrollment.SourceHash).Scan(&recentGlobal, &recentSource); err != nil {
		return err
	}
	if recentGlobal >= 100 || recentSource >= 10 {
		return ErrRateLimited
	}
	requested, _ := json.Marshal(enrollment.RequestedScopes)
	if _, err := tx.Exec(ctx, `
		INSERT INTO access_enrollments
		(id, code_hash, verifier_hash, device_name, platform, model, app_version, public_key, requested_scopes, source_hash, status, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending',$11,$12)
	`, enrollment.ID, enrollment.CodeHash, enrollment.VerifierHash, enrollment.DeviceName,
		enrollment.Platform, enrollment.Model, enrollment.AppVersion, enrollment.PublicKey, requested, enrollment.SourceHash,
		enrollment.CreatedAt, enrollment.ExpiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scanEnrollment(row pgx.Row) (*AccessEnrollment, error) {
	var enrollment AccessEnrollment
	var requested, approved []byte
	err := row.Scan(&enrollment.ID, &enrollment.CodeHash, &enrollment.VerifierHash, &enrollment.DeviceName,
		&enrollment.Platform, &enrollment.Model, &enrollment.AppVersion, &enrollment.PublicKey, &requested, &approved,
		&enrollment.SourceHash, &enrollment.VerifierAttempts,
		&enrollment.Status, &enrollment.DeviceID, &enrollment.CreatedAt, &enrollment.ExpiresAt,
		&enrollment.ApprovedAt, &enrollment.ExchangedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(requested, &enrollment.RequestedScopes)
	_ = json.Unmarshal(approved, &enrollment.ApprovedScopes)
	return &enrollment, nil
}

const enrollmentColumns = `id, code_hash, verifier_hash, device_name, platform, model, app_version, public_key,
	requested_scopes, approved_scopes, source_hash, verifier_attempts, status, device_id, created_at, expires_at, approved_at, exchanged_at`

func (db *DB) GetAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error) {
	return scanEnrollment(db.Pool.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM access_enrollments WHERE id=$1`, id))
}

func (db *DB) GetAccessEnrollmentByCodeHash(ctx context.Context, codeHash string) (*AccessEnrollment, error) {
	return scanEnrollment(db.Pool.QueryRow(ctx, `SELECT `+enrollmentColumns+` FROM access_enrollments WHERE code_hash=$1`, codeHash))
}

func (db *DB) ListAccessEnrollments(ctx context.Context, status string) ([]AccessEnrollment, error) {
	query := `SELECT ` + enrollmentColumns + ` FROM access_enrollments`
	args := []interface{}{}
	if status != "" {
		query += ` WHERE status=$1`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessEnrollment{}
	for rows.Next() {
		enrollment, err := scanEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *enrollment)
	}
	return out, rows.Err()
}

func (db *DB) ApproveAccessEnrollment(ctx context.Context, id, deviceID string, scopes []string) (*AccessEnrollment, error) {
	encoded, _ := json.Marshal(scopes)
	result, err := db.Pool.Exec(ctx, `
		UPDATE access_enrollments SET status='approved', approved_scopes=$1, device_id=$2, approved_at=now()
		WHERE id=$3 AND status='pending' AND expires_at>now()
	`, encoded, deviceID, id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, pgx.ErrNoRows
	}
	return db.GetAccessEnrollment(ctx, id)
}

// ApproveAccessEnrollmentWithDevice atomically creates the device and consumes
// the pending enrollment approval. A concurrent approval cannot leave an
// orphaned device behind.
func (db *DB) ApproveAccessEnrollmentWithDevice(ctx context.Context, id string, device *AccessDevice, scopes []string) (*AccessEnrollment, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	encoded, _ := json.Marshal(scopes)
	result, err := tx.Exec(ctx, `
		UPDATE access_enrollments SET status='approved', approved_scopes=$1, device_id=$2, approved_at=now()
		WHERE id=$3 AND status='pending' AND expires_at>now()
	`, encoded, device.ID, id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access_devices(id,name,platform,model,app_version,public_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
	`, device.ID, device.Name, device.Platform, device.Model, device.AppVersion, device.PublicKey, device.CreatedAt); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return db.GetAccessEnrollment(ctx, id)
}

func (db *DB) ExchangeAccessEnrollment(ctx context.Context, id string) (*AccessEnrollment, error) {
	result, err := db.Pool.Exec(ctx, `
		UPDATE access_enrollments SET status='exchanged', exchanged_at=now()
		WHERE id=$1 AND status='approved' AND expires_at>now()
	`, id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, pgx.ErrNoRows
	}
	return db.GetAccessEnrollment(ctx, id)
}

func (db *DB) RecordAccessEnrollmentFailure(ctx context.Context, id string) error {
	var status string
	err := db.Pool.QueryRow(ctx, `
		UPDATE access_enrollments
		SET verifier_attempts=verifier_attempts+1,
		    status=CASE WHEN verifier_attempts+1>=8 THEN 'locked' ELSE status END
		WHERE id=$1 AND status='approved' AND expires_at>now()
		RETURNING status
	`, id).Scan(&status)
	if err != nil {
		return err
	}
	if status == "locked" {
		return ErrEnrollmentLocked
	}
	return nil
}

func (db *DB) CreateAccessDevice(ctx context.Context, device *AccessDevice) error {
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO access_devices(id,name,platform,model,app_version,public_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
	`, device.ID, device.Name, device.Platform, device.Model, device.AppVersion, device.PublicKey, device.CreatedAt)
	return err
}

func (db *DB) RecordAccessToken(ctx context.Context, token *AccessToken) error {
	scopes, _ := json.Marshal(token.Scopes)
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO access_tokens(jti,device_id,subject,scopes,issued_at,expires_at,rotated_from)
		VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7)
	`, token.JTI, token.DeviceID, token.Subject, scopes, token.IssuedAt, token.ExpiresAt, token.RotatedFrom)
	return err
}

func insertAccessToken(ctx context.Context, tx pgx.Tx, token *AccessToken) error {
	scopes, _ := json.Marshal(token.Scopes)
	_, err := tx.Exec(ctx, `
		INSERT INTO access_tokens(jti,device_id,subject,scopes,issued_at,expires_at,rotated_from)
		VALUES($1,NULLIF($2,''),$3,$4,$5,$6,$7)
	`, token.JTI, token.DeviceID, token.Subject, scopes, token.IssuedAt, token.ExpiresAt, token.RotatedFrom)
	return err
}

// ExchangeAccessEnrollmentWithToken makes the one-time enrollment exchange and
// token record durable in the same transaction. The plaintext bearer token is
// returned only after this transaction commits and is never persisted.
func (db *DB) ExchangeAccessEnrollmentWithToken(ctx context.Context, id string, token *AccessToken) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE access_enrollments SET status='exchanged', exchanged_at=now()
		WHERE id=$1 AND status='approved' AND expires_at>now()
	`, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := insertAccessToken(ctx, tx, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RotateAccessToken atomically records the replacement and revokes the token
// that authorized rotation.
func (db *DB) RotateAccessToken(ctx context.Context, previousJTI string, token *AccessToken) ([]string, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `
		UPDATE access_tokens SET revoked_at=now()
		WHERE jti=$1 AND revoked_at IS NULL AND expires_at>now()
	`, previousJTI)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, pgx.ErrNoRows
	}
	if err := insertAccessToken(ctx, tx, token); err != nil {
		return nil, err
	}
	sessions, err := cancelActiveExecSessionsTx(ctx, tx, "token_jti", previousJTI, "token_rotated")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (db *DB) AccessTokenActive(ctx context.Context, jti string) (bool, error) {
	var active bool
	err := db.Pool.QueryRow(ctx, `
		SELECT revoked_at IS NULL AND expires_at>now() AND (device_id IS NULL OR NOT EXISTS(
			SELECT 1 FROM access_devices d WHERE d.id=access_tokens.device_id AND d.revoked_at IS NOT NULL
		)) FROM access_tokens WHERE jti=$1
	`, jti).Scan(&active)
	return active, err
}

func (db *DB) RevokeAccessToken(ctx context.Context, jti string) ([]string, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE jti=$1`, jti)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, pgx.ErrNoRows
	}
	sessions, err := cancelActiveExecSessionsTx(ctx, tx, "token_jti", jti, "token_revoked")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (db *DB) RevokeAccessDevice(ctx context.Context, id string) ([]string, error) {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE access_devices SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE device_id=$1`, id); err != nil {
		return nil, err
	}
	sessions, err := cancelActiveExecSessionsTx(ctx, tx, "device_id", id, "device_revoked")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (db *DB) TouchAccessDevice(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `
		UPDATE access_devices SET last_seen_at=now()
		WHERE id=$1 AND revoked_at IS NULL AND (last_seen_at IS NULL OR last_seen_at < now()-interval '5 minutes')
	`, id)
	return err
}

func (db *DB) ListAccessDevices(ctx context.Context) ([]AccessDevice, error) {
	rows, err := db.Pool.Query(ctx, `SELECT id,name,platform,model,app_version,public_key,created_at,last_seen_at,revoked_at FROM access_devices ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessDevice{}
	for rows.Next() {
		var device AccessDevice
		if err := rows.Scan(&device.ID, &device.Name, &device.Platform, &device.Model, &device.AppVersion, &device.PublicKey, &device.CreatedAt, &device.LastSeenAt, &device.RevokedAt); err != nil {
			return nil, err
		}
		device.Tokens = []AccessToken{}
		tokenRows, tokenErr := db.Pool.Query(ctx, `SELECT jti,device_id,subject,scopes,issued_at,expires_at,revoked_at,rotated_from FROM access_tokens WHERE device_id=$1 ORDER BY issued_at DESC`, device.ID)
		if tokenErr != nil {
			return nil, tokenErr
		}
		for tokenRows.Next() {
			var token AccessToken
			var scopes []byte
			if err := tokenRows.Scan(&token.JTI, &token.DeviceID, &token.Subject, &scopes, &token.IssuedAt, &token.ExpiresAt, &token.RevokedAt, &token.RotatedFrom); err != nil {
				tokenRows.Close()
				return nil, err
			}
			_ = json.Unmarshal(scopes, &token.Scopes)
			device.Tokens = append(device.Tokens, token)
		}
		tokenRows.Close()
		out = append(out, device)
	}
	return out, rows.Err()
}
