//go:build !linux

package pipeline

import "testing"

func configureProcessCrashMigrationEffects(t *testing.T, _ *Pipeline) {
	t.Helper()
	t.Fatal("migration cgroup test requires Linux")
}
