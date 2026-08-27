package worker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"norn/v2/api/model"
)

func TestMaintenanceExecutorUsesOnlyAllowlistedCommands(t *testing.T) {
	dir := t.TempDir()
	platform := filepath.Join(dir, "platform-upgrade")
	if err := os.WriteFile(platform, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor := &CommandMaintenanceExecutor{PlatformScript: platform}
	op := &model.Operation{ID: "op-1", Kind: "platform.upgrade", Payload: map[string]interface{}{
		"ref": "v2.16.0", "mode": "proxy", "drainMode": "wait",
	}}
	program, args, env, err := executor.command(op)
	if err != nil {
		t.Fatal(err)
	}
	if program != platform || !slices.Equal(args, []string{"upgrade", "v2.16.0"}) {
		t.Fatalf("command = %q %#v", program, args)
	}
	for _, expected := range []string{"NORN_DRAIN_EXCLUDE_OPERATION_ID=op-1", "NORN_PLATFORM_UPGRADE_MODE=proxy", "NORN_DRAIN_MODE=wait"} {
		if !slices.Contains(env, expected) {
			t.Errorf("environment missing %q: %#v", expected, env)
		}
	}
}

func TestMaintenanceExecutorRejectsTamperedPayload(t *testing.T) {
	dir := t.TempDir()
	platform := filepath.Join(dir, "platform-upgrade")
	if err := os.WriteFile(platform, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	executor := &CommandMaintenanceExecutor{PlatformScript: platform}
	for _, op := range []*model.Operation{
		{Kind: "platform.upgrade", Payload: map[string]interface{}{"ref": "--exec=/tmp/x", "mode": "restart", "drainMode": "fail"}},
		{Kind: "platform.upgrade", Payload: map[string]interface{}{"ref": "HEAD", "mode": "shell", "drainMode": "fail"}},
		{Kind: "platform.rollback", Payload: map[string]interface{}{"sha": "--force"}},
		{Kind: "platform.rollback", Payload: map[string]interface{}{"sha": "a/../../outside"}},
		{Kind: "platform.rollback", Payload: map[string]interface{}{"sha": "abcdef"}},
		{Kind: "host.shell", Payload: map[string]interface{}{}},
	} {
		if _, _, _, err := executor.command(op); err == nil {
			t.Fatalf("expected rejection for %#v", op)
		}
	}
}
