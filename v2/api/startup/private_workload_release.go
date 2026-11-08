package startup

import (
	"context"
	"errors"
	"fmt"

	"norn/v2/api/store"
)

// PreflightPrivateWorkloadReleaseKeys prevents a controller from accepting
// release work when it cannot reopen retained encrypted input snapshots.
func PreflightPrivateWorkloadReleaseKeys(ctx context.Context, keyStore store.PrivateWorkloadReleaseKeyStore, keys *store.WorkloadReleaseInputKeyRing) error {
	if keyStore == nil {
		return errors.New("private workload release key store is unavailable")
	}
	if keys == nil {
		return errors.New("private workload release key ring is unavailable")
	}
	required, err := keyStore.RequiredWorkloadReleaseInputKeys(ctx)
	if err != nil {
		return fmt.Errorf("read required private workload release keys: %w", err)
	}
	if err := keys.RequireWorkloadReleaseInputKeys(required); err != nil {
		return fmt.Errorf("require private workload release keys: %w", err)
	}
	return nil
}
