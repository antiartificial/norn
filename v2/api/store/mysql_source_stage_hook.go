//go:build !norn_test_crash_hooks

package store

func afterMySQLSourceStageDump(string, string) error { return nil }
