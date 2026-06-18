package engine

import (
	"testing"
)

func TestServiceHealthChecks_Empty(t *testing.T) {
	e := &Engine{
		instances: make(map[string]*Instance),
		health:    make(map[string]*healthState),
	}
	checks, err := e.ServiceHealthChecks("myapp-web")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 0 {
		t.Errorf("expected 0 checks, got %d", len(checks))
	}
}

func TestServiceHealthChecks_MatchesRunning(t *testing.T) {
	healthy := true
	e := &Engine{
		instances: map[string]*Instance{
			"norn-myapp-web-0": {
				ContainerName: "norn-myapp-web-0",
				App:           "myapp",
				Process:       "web",
				Status:        "running",
				Healthy:       &healthy,
				IP:            "192.168.64.5",
				Port:          8080,
			},
			"norn-myapp-web-1": {
				ContainerName: "norn-myapp-web-1",
				App:           "myapp",
				Process:       "web",
				Status:        "stopped",
				IP:            "192.168.64.6",
				Port:          8080,
			},
			"norn-other-web-0": {
				ContainerName: "norn-other-web-0",
				App:           "other",
				Process:       "web",
				Status:        "running",
				IP:            "192.168.64.7",
				Port:          8080,
			},
		},
		health: map[string]*healthState{
			"norn-myapp-web-0": {status: "passing"},
		},
	}

	checks, err := e.ServiceHealthChecks("myapp-web")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("expected 1 check (only running), got %d", len(checks))
	}
	if checks[0].Address != "192.168.64.5" {
		t.Errorf("address = %q", checks[0].Address)
	}
	if checks[0].Status != "passing" {
		t.Errorf("status = %q", checks[0].Status)
	}
}

func TestServiceAddress_PrefersHealthy(t *testing.T) {
	e := &Engine{
		instances: map[string]*Instance{
			"norn-myapp-web-0": {
				ContainerName: "norn-myapp-web-0",
				App:           "myapp",
				Process:       "web",
				Status:        "running",
				IP:            "192.168.64.5",
				Port:          8080,
			},
			"norn-myapp-web-1": {
				ContainerName: "norn-myapp-web-1",
				App:           "myapp",
				Process:       "web",
				Status:        "running",
				IP:            "192.168.64.6",
				Port:          8080,
			},
		},
		health: map[string]*healthState{
			"norn-myapp-web-0": {status: "critical"},
			"norn-myapp-web-1": {status: "passing"},
		},
	}

	addr, err := e.ServiceAddress("myapp-web")
	if err != nil {
		t.Fatal(err)
	}
	if addr != "192.168.64.6:8080" {
		t.Errorf("addr = %q, want 192.168.64.6:8080", addr)
	}
}

func TestServiceInstances(t *testing.T) {
	e := &Engine{
		instances: map[string]*Instance{
			"norn-myapp-web-0": {
				ContainerName: "norn-myapp-web-0",
				App:           "myapp",
				Process:       "web",
				Status:        "running",
				IP:            "192.168.64.5",
				Port:          8080,
			},
		},
		health: map[string]*healthState{},
	}

	instances, err := e.ServiceInstances("myapp-web")
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(instances))
	}
	if instances[0].Address != "192.168.64.5" || instances[0].Port != 8080 {
		t.Errorf("instance = %+v", instances[0])
	}
}
