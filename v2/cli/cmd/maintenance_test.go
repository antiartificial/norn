package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMaintenanceRequestKeyPrintsExplicitRetryKey(t *testing.T) {
	original := maintenanceIdempotencyKey
	t.Cleanup(func() { maintenanceIdempotencyKey = original })
	maintenanceIdempotencyKey = "  stable-retry-key  "
	command := &cobra.Command{}
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	key, err := maintenanceRequestKey(command)
	if err != nil {
		t.Fatal(err)
	}
	if key != "stable-retry-key" || !strings.Contains(stderr.String(), key) {
		t.Fatalf("key=%q stderr=%q", key, stderr.String())
	}
}

func TestMaintenanceRequestKeyGeneratesAndPrintsReusableKey(t *testing.T) {
	original := maintenanceIdempotencyKey
	t.Cleanup(func() { maintenanceIdempotencyKey = original })
	maintenanceIdempotencyKey = ""
	command := &cobra.Command{}
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	key, err := maintenanceRequestKey(command)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "norn-maintenance-") || !strings.Contains(stderr.String(), key) {
		t.Fatalf("key=%q stderr=%q", key, stderr.String())
	}
}

func TestMaintenanceRequestKeyRejectsOversizeValue(t *testing.T) {
	original := maintenanceIdempotencyKey
	t.Cleanup(func() { maintenanceIdempotencyKey = original })
	maintenanceIdempotencyKey = strings.Repeat("x", 201)
	if _, err := maintenanceRequestKey(&cobra.Command{}); err == nil {
		t.Fatal("expected oversized idempotency key rejection")
	}
}
