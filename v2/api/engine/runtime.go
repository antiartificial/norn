package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// EnsureRuntime starts Apple's container system when the explicit local
// connector is selected, then waits for a bounded readiness result. It is not
// used by the Nomad/Consul connector.
func EnsureRuntime(ctx context.Context) error {
	if runtimeRunning(ctx) {
		return nil
	}
	if _, err := containerCmd(ctx, "system", "start"); err != nil {
		return fmt.Errorf("start apple container system: %w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for apple container system: %w", ctx.Err())
		case <-ticker.C:
			if runtimeRunning(ctx) {
				return nil
			}
		}
	}
}

func runtimeRunning(ctx context.Context) bool {
	out, err := containerCmd(ctx, "system", "status", "--format", "json")
	if err != nil {
		return false
	}
	value := strings.ToLower(string(out))
	if strings.Contains(value, "not running") || strings.Contains(value, "stopped") || strings.Contains(value, "unregistered") {
		return false
	}
	return strings.Contains(value, "running") || strings.Contains(value, "ready")
}
