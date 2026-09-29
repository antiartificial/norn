//go:build norn_test_crash_hooks

package store

import (
	"os"
	"time"
)

// Only the disposable process-kill fixture uses this marker. PostgreSQL has
// all transfer writes in one uncommitted transaction when the process pauses.
func beforeMySQLSourceReconcileCommit(operationID string) error {
	path := os.Getenv("NORN_TEST_SOURCE_BEFORE_COMMIT_MARKER")
	if path == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(operationID), 0o600); err != nil {
		return err
	}
	time.Sleep(2 * time.Minute)
	return nil
}
