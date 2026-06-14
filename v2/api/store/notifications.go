package store

import (
	"context"
	"encoding/json"

	"norn/v2/api/model"
)

func (db *DB) InsertNotificationChannel(ctx context.Context, ch *model.NotificationChannel) error {
	severities, err := json.Marshal(ch.Severities)
	if err != nil {
		return err
	}
	if ch.Severities == nil {
		severities = []byte("[]")
	}
	_, err = db.Pool.Exec(ctx, `
		INSERT INTO notification_channels (id, provider, name, url, token, user_key, severities, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, ch.ID, ch.Provider, ch.Name, ch.URL, ch.Token, ch.UserKey, severities, ch.CreatedAt)
	return err
}

func (db *DB) ListNotificationChannels(ctx context.Context) ([]model.NotificationChannel, error) {
	rows, err := db.Pool.Query(ctx, `
		SELECT id, provider, name, url, token, user_key, severities, created_at
		FROM notification_channels
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []model.NotificationChannel
	for rows.Next() {
		var ch model.NotificationChannel
		var severities []byte
		if err := rows.Scan(&ch.ID, &ch.Provider, &ch.Name, &ch.URL, &ch.Token, &ch.UserKey, &severities, &ch.CreatedAt); err != nil {
			return nil, err
		}
		if len(severities) > 0 {
			_ = json.Unmarshal(severities, &ch.Severities)
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (db *DB) GetNotificationChannel(ctx context.Context, id string) (*model.NotificationChannel, error) {
	var ch model.NotificationChannel
	var severities []byte
	err := db.Pool.QueryRow(ctx, `
		SELECT id, provider, name, url, token, user_key, severities, created_at
		FROM notification_channels
		WHERE id = $1
	`, id).Scan(&ch.ID, &ch.Provider, &ch.Name, &ch.URL, &ch.Token, &ch.UserKey, &severities, &ch.CreatedAt)
	if err != nil {
		return nil, err
	}
	if len(severities) > 0 {
		_ = json.Unmarshal(severities, &ch.Severities)
	}
	return &ch, nil
}

func (db *DB) DeleteNotificationChannel(ctx context.Context, id string) error {
	_, err := db.Pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
	return err
}
