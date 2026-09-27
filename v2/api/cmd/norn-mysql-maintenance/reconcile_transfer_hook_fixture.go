//go:build norn_test_crash_hooks

package main

import (
	"os"
	"time"
)

// This build exists only in the disposable process-kill fixture. The marker
// reports that the transfer returned; the harness kills this exact process
// before it can start the account-lock effect.
func afterReconcileSourceTransfer(operationID string) error {
	path := os.Getenv("NORN_TEST_SOURCE_TRANSFER_MARKER")
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(operationID), 0o600); err != nil {
		return err
	}
	time.Sleep(2 * time.Minute)
	return nil
}
