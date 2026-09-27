package ingress

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"norn/v2/api/nomad"
)

func TestRenderWeightedRouteBindsDistinctRevisionsWithoutPublicBackendRouter(t *testing.T) {
	input := WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 70}, {DeploymentID: "new", Weight: 30}}}
	result, err := RenderWeightedRoute(input)
	if err != nil {
		t.Fatal(err)
	}
	var doc routeDocument
	if err := yaml.Unmarshal(result.YAML, &doc); err != nil {
		t.Fatal(err)
	}
	router := doc.HTTP.Routers[result.RouterName]
	if router.Rule != "Host(`orders.example.com`)" || router.Service != result.ServiceName || len(router.EntryPoints) != 1 || router.EntryPoints[0] != "websecure" || router.TLS == nil {
		t.Fatalf("public router does not target weighted HTTPS service: %+v", router)
	}
	backends := doc.HTTP.Services[result.ServiceName].Weighted.Services
	if len(backends) != 2 || backends[0].Weight != 30 || backends[1].Weight != 70 {
		t.Fatalf("weighted backends=%+v", backends)
	}
	for i, id := range []string{"new", "old"} {
		name, err := nomad.ManagedBackendServiceName("orders", "web", "iad", id)
		if err != nil || backends[i].Name != name+"@consulcatalog" {
			t.Fatalf("backend %d does not reference translated service: %+v, %v", i, backends[i], err)
		}
	}
	repeated, err := RenderWeightedRoute(input)
	if err != nil || repeated.SHA256 != result.SHA256 || !bytes.Equal(repeated.YAML, result.YAML) {
		t.Fatalf("rendered route is not deterministic: %v", err)
	}
	input.Backends[0], input.Backends[1] = input.Backends[1], input.Backends[0]
	reordered, err := RenderWeightedRoute(input)
	if err != nil || reordered.SHA256 != result.SHA256 || !bytes.Equal(reordered.YAML, result.YAML) {
		t.Fatalf("backend order changed desired route revision: %v", err)
	}
	if strings.Contains(string(result.YAML), "norn.invalid") {
		t.Fatal("public file route referenced private placeholder hostname")
	}
}

func TestRenderWeightedRouteRejectsUnsafeOrAmbiguousPlan(t *testing.T) {
	base := WeightedRoute{App: "orders", Process: "web", Region: "iad", Endpoint: "https://orders.example.com",
		Backends: []WeightedBackend{{DeploymentID: "old", Weight: 100}}}
	cases := []struct {
		name   string
		change func(*WeightedRoute)
	}{
		{"missing revision", func(r *WeightedRoute) { r.Backends[0].DeploymentID = "" }},
		{"partial weight", func(r *WeightedRoute) { r.Backends[0].Weight = 90 }},
		{"duplicate revision", func(r *WeightedRoute) { r.Backends = append(r.Backends, r.Backends[0]) }},
		{"host injection", func(r *WeightedRoute) { r.Endpoint = "https://orders.example.com`) || Host(`evil.example.com" }},
		{"endpoint path", func(r *WeightedRoute) { r.Endpoint += "/private" }},
		{"credential URL", func(r *WeightedRoute) { r.Endpoint = "https://user:pass@orders.example.com" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base
			candidate.Backends = append([]WeightedBackend(nil), base.Backends...)
			tc.change(&candidate)
			if _, err := RenderWeightedRoute(candidate); err == nil {
				t.Fatal("invalid route plan rendered")
			}
		})
	}
}
