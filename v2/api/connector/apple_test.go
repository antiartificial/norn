package connector

import (
	"context"
	"strings"
	"testing"

	"norn/v2/api/engine"
	"norn/v2/api/model"
)

func TestAppleConnectorRejectsProductionAndRegionalSpecs(t *testing.T) {
	connector := NewApple(&engine.Engine{})
	local := &model.InfraSpec{App: "sample", Processes: map[string]model.Process{"worker": {}}}
	if err := connector.Validate(local, true); err == nil || !strings.Contains(err.Error(), "development-only") {
		t.Fatalf("production validation error = %v", err)
	}
	regional := &model.InfraSpec{App: "sample", Processes: map[string]model.Process{"worker": {}}, Regions: map[string]model.RegionTarget{"west": {}, "east": {}}}
	if err := connector.Validate(regional, false); err == nil || !strings.Contains(err.Error(), "implicit local region") {
		t.Fatalf("regional validation error = %v", err)
	}
}

func TestAppleConnectorRejectsUnbalancedLocalEndpoints(t *testing.T) {
	connector := NewApple(&engine.Engine{})
	spec := &model.InfraSpec{App: "sample", Processes: map[string]model.Process{
		"web": {Port: 8080, Scaling: &model.Scaling{Min: 2}},
	}}
	if err := connector.Validate(spec, false); err == nil || !strings.Contains(err.Error(), "external local ingress") {
		t.Fatalf("endpoint scaling validation error = %v", err)
	}
}

func TestAppleConnectorResolvesEndpointToLoopbackHostPort(t *testing.T) {
	connector := NewApple(&engine.Engine{})
	spec := &model.InfraSpec{App: "sample", Processes: map[string]model.Process{
		"web": {Port: 8080, HostPort: 18080},
	}}
	origin, err := connector.EndpointOrigin(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "http://127.0.0.1:18080" {
		t.Fatalf("endpoint origin = %q", origin)
	}
}
