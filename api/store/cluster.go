package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"norn/api/model"
)

func (db *DB) InsertClusterNode(ctx context.Context, n *model.ClusterNode) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO cluster_nodes (id, name, provider, region, size, role, public_ip, tailscale_ip, status, provider_id, error, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		n.ID, n.Name, n.Provider, n.Region, n.Size, n.Role, n.PublicIP, n.TailscaleIP, n.Status, n.ProviderID, n.Error, n.CreatedAt,
	)
	return err
}

func (db *DB) UpdateClusterNodeStatus(ctx context.Context, id string, status string, errMsg string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE cluster_nodes SET status = $1, error = $2, updated_at = now() WHERE id = $3`,
		status, errMsg, id,
	)
	return err
}

func (db *DB) ListClusterNodes(ctx context.Context) ([]model.ClusterNode, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT id, name, provider, region, size, role, public_ip, tailscale_ip, status, provider_id, error, created_at
		 FROM cluster_nodes ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []model.ClusterNode
	for rows.Next() {
		var n model.ClusterNode
		if err := rows.Scan(&n.ID, &n.Name, &n.Provider, &n.Region, &n.Size, &n.Role, &n.PublicIP, &n.TailscaleIP, &n.Status, &n.ProviderID, &n.Error, &n.CreatedAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (db *DB) GetClusterNode(ctx context.Context, id string) (*model.ClusterNode, error) {
	var n model.ClusterNode
	err := db.pool.QueryRow(ctx,
		`SELECT id, name, provider, region, size, role, public_ip, tailscale_ip, status, provider_id, error, created_at
		 FROM cluster_nodes WHERE id = $1`, id,
	).Scan(&n.ID, &n.Name, &n.Provider, &n.Region, &n.Size, &n.Role, &n.PublicIP, &n.TailscaleIP, &n.Status, &n.ProviderID, &n.Error, &n.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &n, nil
}

func (db *DB) DeleteClusterNode(ctx context.Context, id string) error {
	_, err := db.pool.Exec(ctx,
		`DELETE FROM cluster_nodes WHERE id = $1`, id,
	)
	return err
}
