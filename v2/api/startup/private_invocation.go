package startup

import (
	"context"
	"errors"
	"fmt"

	"norn/v2/api/store"
)

// PreflightPrivateInvocationKeys verifies that a restored control store has
// every private-invocation envelope key required to read retained records.
// It is intentionally backend-neutral: both PostgreSQL and etcd operation
// stores provide this read-only preflight through PrivateInvocationStore.
// Callers opt into it during a future startup or restore flow.
func PreflightPrivateInvocationKeys(ctx context.Context, invocationStore store.PrivateInvocationStore, keys *store.PrivateInvocationKeyRing) error {
	if invocationStore == nil {
		return errors.New("private invocation store is unavailable")
	}
	if keys == nil {
		return errors.New("private invocation key ring is unavailable")
	}
	required, err := invocationStore.RequiredPrivateInvocationKeys(ctx)
	if err != nil {
		return fmt.Errorf("read required private invocation keys: %w", err)
	}
	if err := keys.RequirePrivateInvocationKeys(required); err != nil {
		return fmt.Errorf("require private invocation keys: %w", err)
	}
	return nil
}
