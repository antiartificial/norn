package store

import (
	"context"
	"strings"
)

type DatabaseRecoveryStatus struct {
	ArchiveMode       string `json:"archiveMode"`
	ArchiveCommand    string `json:"-"`
	ArchiveLibrary    string `json:"-"`
	WALLevel          string `json:"walLevel"`
	ServerAddress     string `json:"serverAddress,omitempty"`
	StreamingReplicas int    `json:"streamingReplicas"`
}

func (s DatabaseRecoveryStatus) PITREnabled() bool {
	mode := strings.ToLower(strings.TrimSpace(s.ArchiveMode))
	archiveConfigured := configuredPostgresArchiveSetting(s.ArchiveCommand) || configuredPostgresArchiveSetting(s.ArchiveLibrary)
	walLevel := strings.ToLower(strings.TrimSpace(s.WALLevel))
	return (mode == "on" || mode == "always") && archiveConfigured && (walLevel == "replica" || walLevel == "logical")
}

func configuredPostgresArchiveSetting(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "(disabled)" && value != "false" && value != "off"
}

func (db *DB) InspectDatabaseRecovery(ctx context.Context) (*DatabaseRecoveryStatus, error) {
	var status DatabaseRecoveryStatus
	err := db.Pool.QueryRow(ctx, `
		SELECT
			coalesce(current_setting('archive_mode', true), ''),
			coalesce(current_setting('archive_command', true), ''),
			coalesce(current_setting('archive_library', true), ''),
			coalesce(current_setting('wal_level', true), ''),
			coalesce(inet_server_addr()::text, '')
	`).Scan(&status.ArchiveMode, &status.ArchiveCommand, &status.ArchiveLibrary, &status.WALLevel, &status.ServerAddress)
	if err != nil {
		return nil, err
	}
	if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_replication WHERE state='streaming'`).Scan(&status.StreamingReplicas); err != nil {
		return nil, err
	}
	return &status, nil
}
