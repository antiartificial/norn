//go:build norn_test_crash_hooks

package store

import (
	"os"
	"time"
)

// The disposable fixture kills the command after the dump tool has produced
// local bytes but before any signed stage receipt is committed.
func afterMySQLSourceStageDump(operationID, path string) error {
	marker := os.Getenv("NORN_TEST_SOURCE_STAGE_DUMP_MARKER")
	if marker == "" {
		return nil
	}
	if err := os.WriteFile(marker, []byte(operationID+"\n"+path), 0o600); err != nil {
		return err
	}
	time.Sleep(2 * time.Minute)
	return nil
}

func atMySQLSourcePublicationCheckpoint(operationID, phase string) error {
	key := "NORN_TEST_SOURCE_PUBLISH_BEFORE_UPLOAD_MARKER"
	if phase == "verified" {
		key = "NORN_TEST_SOURCE_PUBLISH_AFTER_VERIFY_MARKER"
	}
	marker := os.Getenv(key)
	if marker == "" {
		return nil
	}
	if err := os.WriteFile(marker, []byte(operationID), 0o600); err != nil {
		return err
	}
	time.Sleep(2 * time.Minute)
	return nil
}
