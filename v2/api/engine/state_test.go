package engine

import (
	"testing"
	"time"
)

func TestInstance_IsRunning(t *testing.T) {
	inst := &Instance{Status: "running"}
	if !inst.IsRunning() {
		t.Error("expected running")
	}
	inst.Status = "stopped"
	if inst.IsRunning() {
		t.Error("expected not running")
	}
}

func TestInstance_IsTerminal(t *testing.T) {
	for _, status := range []string{"stopped", "failed"} {
		inst := &Instance{Status: status}
		if !inst.IsTerminal() {
			t.Errorf("expected %q to be terminal", status)
		}
	}
	inst := &Instance{Status: "running"}
	if inst.IsTerminal() {
		t.Error("running should not be terminal")
	}
}

func TestInstance_ToAllocation(t *testing.T) {
	healthy := true
	now := time.Now()
	inst := &Instance{
		ContainerName: "norn-myapp-web-0",
		App:           "myapp",
		Process:       "web",
		Status:        "running",
		Healthy:       &healthy,
		IP:            "192.168.64.5",
		StartedAt:     now,
	}

	a := inst.ToAllocation()
	if a.ID != "norn-mya" {
		t.Errorf("ID = %q, want norn-mya", a.ID)
	}
	if a.TaskGroup != "web" {
		t.Errorf("TaskGroup = %q, want web", a.TaskGroup)
	}
	if a.Status != "running" {
		t.Errorf("Status = %q, want running", a.Status)
	}
	if a.Lifecycle != "active" {
		t.Errorf("Lifecycle = %q, want active", a.Lifecycle)
	}
	if a.NodeAddress != "192.168.64.5" {
		t.Errorf("NodeAddress = %q, want 192.168.64.5", a.NodeAddress)
	}
	if a.NodeProvider != "local" {
		t.Errorf("NodeProvider = %q, want local", a.NodeProvider)
	}
	if a.StartedAt == "" {
		t.Error("StartedAt should not be empty")
	}

	// Terminal instance
	inst.Status = "stopped"
	a = inst.ToAllocation()
	if a.Lifecycle != "retained" {
		t.Errorf("stopped Lifecycle = %q, want retained", a.Lifecycle)
	}
}
