//go:build !norn_test_crash_hooks

package main

func afterReconcileSourceTransfer(string) error { return nil }
