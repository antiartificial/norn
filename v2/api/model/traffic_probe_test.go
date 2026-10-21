package model

import (
	"slices"
	"strings"
	"testing"
)

func TestFleetTrafficProbeRequiresCanonicalSignedResponse(t *testing.T) {
	probe := &TrafficProbeSpec{Path: "/route-proof", BodySHA256: strings.Repeat("a", 64)}
	if err := ValidateTrafficProbe(probe); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrafficProbe(&TrafficProbeSpec{Path: "/", BodySHA256: probe.BodySHA256}); err != nil {
		t.Fatalf("root endpoint probe was rejected: %v", err)
	}
	spec := &InfraSpec{App: "pilot", Processes: map[string]Process{"web": {Port: 8080}}, Endpoints: []Endpoint{{URL: "https://pilot.example.test", Region: "west", Process: "web", TrafficProbe: probe}}}
	endpoint, err := ResolveFleetRouteEndpoint(spec, "west")
	if err != nil || endpoint.ProbePath != probe.Path || endpoint.ProbeBodySHA256 != probe.BodySHA256 {
		t.Fatalf("resolved signed probe=%+v err=%v", endpoint, err)
	}
	for _, invalid := range []TrafficProbeSpec{{Path: "//other", BodySHA256: probe.BodySHA256}, {Path: "/ready?x=1", BodySHA256: probe.BodySHA256}, {Path: "/ready", BodySHA256: strings.Repeat("A", 64)}} {
		spec.Endpoints[0].TrafficProbe = &invalid
		if _, err := ResolveFleetRouteEndpoint(spec, "west"); err == nil {
			t.Fatalf("invalid probe accepted: %+v", invalid)
		}
	}
}

func TestResolveFleetRouteEndpointPreservesRestrictedIngress(t *testing.T) {
	policy := &RestrictedIngressSpec{PublicPaths: []string{"/records/", "/version"}, RetryGETPaths: []string{"/records/"}, PrivateReadinessPath: "/readyz"}
	spec := &InfraSpec{App: "pilot", Processes: map[string]Process{"web": {Port: 8080, Health: &HealthSpec{Path: "/readyz"}}}, Endpoints: []Endpoint{{URL: "https://pilot.example.test", Region: "west", Process: "web", RestrictedIngress: policy}}}
	endpoint, err := ResolveFleetRouteEndpoint(spec, "west")
	if err != nil || !slices.Equal(endpoint.PublicPaths, policy.PublicPaths) || !slices.Equal(endpoint.RetryGETPaths, policy.RetryGETPaths) || endpoint.PrivateReadinessPath != policy.PrivateReadinessPath {
		t.Fatalf("resolved restricted ingress=%+v err=%v", endpoint, err)
	}
	policy.PublicPaths[0] = "/changed"
	policy.RetryGETPaths[0] = "/changed"
	if endpoint.PublicPaths[0] != "/records/" || endpoint.RetryGETPaths[0] != "/records/" {
		t.Fatal("resolved route policy aliases mutable InfraSpec slices")
	}
}
