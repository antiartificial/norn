//go:build !norn_test_crash_hooks

package store

func beforeMySQLSourceReconcileCommit(string) error { return nil }
