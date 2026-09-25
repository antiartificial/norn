package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RootAccessToken returns the first credential in a managed token's durable
// rotation chain. Missing ancestors, cycles, and excessively deep chains are
// ambiguous and must not be accepted as a stable operation actor.
func (db *DB) RootAccessToken(ctx context.Context, current string) (string, error) {
	if db == nil || db.Pool == nil {
		return "", fmt.Errorf("managed token lineage store is unavailable")
	}
	seen := make(map[string]struct{}, 8)
	for depth := 0; depth < 64; depth++ {
		current = strings.TrimSpace(current)
		if current == "" {
			return "", fmt.Errorf("managed token lineage has an empty identifier")
		}
		if _, exists := seen[current]; exists {
			return "", fmt.Errorf("managed token lineage contains a cycle")
		}
		seen[current] = struct{}{}
		var parent string
		err := db.Pool.QueryRow(ctx, `SELECT rotated_from FROM access_tokens WHERE jti=$1`, current).Scan(&parent)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("managed token lineage is incomplete: %w", ErrIdentityNotFound)
		}
		if err != nil {
			return "", fmt.Errorf("managed token lineage lookup failed: %w", err)
		}
		if strings.TrimSpace(parent) == "" {
			return current, nil
		}
		current = parent
	}
	return "", fmt.Errorf("managed token lineage exceeds 64 links")
}
