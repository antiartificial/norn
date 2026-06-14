package store

import (
	"context"
	"fmt"
	"time"
)

type AccessGrant struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	Note      string    `json:"note"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (db *DB) ListAccessGrants(ctx context.Context) ([]AccessGrant, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, ip, note, created_by, created_at, expires_at
		FROM access_grants
		WHERE expires_at > now()
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []AccessGrant
	for rows.Next() {
		var g AccessGrant
		if err := rows.Scan(&g.ID, &g.IP, &g.Note, &g.CreatedBy, &g.CreatedAt, &g.ExpiresAt); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

func (db *DB) CreateAccessGrant(ctx context.Context, g *AccessGrant) error {
	if g.ID == "" {
		g.ID = fmt.Sprintf("ag_%d", time.Now().UnixNano())
	}
	_, err := db.Pool.Exec(ctx, `
		INSERT INTO access_grants (id, ip, note, created_by, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, g.ID, g.IP, g.Note, g.CreatedBy, g.CreatedAt, g.ExpiresAt)
	return err
}

func (db *DB) DeleteAccessGrant(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM access_grants WHERE id = $1`, id)
	return err
}

func (db *DB) MatchAccessGrant(ctx context.Context, ip string) (bool, error) {
	var matched bool
	err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM access_grants
			WHERE ip = $1 AND expires_at > now()
		)
	`, ip).Scan(&matched)
	if err != nil {
		return false, err
	}
	return matched, nil
}

func (db *DB) CleanExpiredGrants(ctx context.Context) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM access_grants WHERE expires_at <= now()`)
	return err
}
