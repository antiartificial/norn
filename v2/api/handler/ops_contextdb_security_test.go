package handler

import (
	"testing"

	"norn/v2/api/model"
)

func TestFirstPrivateServiceInstanceURLRejectsPublicTargets(t *testing.T) {
	service := model.ServiceManifestEntry{Instances: []model.ServiceInstance{
		{Address: "203.0.113.10", Port: 7701},
		{Address: "metadata.google.internal", Port: 80},
		{Address: "192.168.4.124", Port: 7701},
	}}
	if got := firstPrivateServiceInstanceURL(service); got != "http://192.168.4.124:7701" {
		t.Fatalf("internal service URL = %q", got)
	}
	service.Instances = []model.ServiceInstance{{Address: "100.64.12.3", Port: 7701}}
	if got := firstPrivateServiceInstanceURL(service); got != "http://100.64.12.3:7701" {
		t.Fatalf("tailnet service URL = %q", got)
	}
	service.Instances = []model.ServiceInstance{{Address: "203.0.113.10", Port: 7701}}
	if got := firstPrivateServiceInstanceURL(service); got != "" {
		t.Fatalf("public target must be rejected, got %q", got)
	}
}

func TestContextDBIdentifiersRejectPathSyntax(t *testing.T) {
	for _, value := range []string{"hermes-agent", "event_123", "namespace.v2"} {
		if !contextDBIdentifierPattern.MatchString(value) {
			t.Fatalf("expected %q to validate", value)
		}
	}
	for _, value := range []string{"../event", "namespace/event", "", "event?redirect=http://example.test"} {
		if contextDBIdentifierPattern.MatchString(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
