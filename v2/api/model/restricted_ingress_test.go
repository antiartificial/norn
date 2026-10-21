package model

import "testing"

func TestRestrictedIngressRejectsUnreviewedPublicRoutes(t *testing.T) {
	base := func() *InfraSpec {
		return &InfraSpec{
			App: "pilot", Processes: map[string]Process{"web": {Port: 8080, Health: &HealthSpec{Path: "/readyz"}}},
			Endpoints: []Endpoint{{URL: "https://pilot.example.test", Process: "web", RestrictedIngress: &RestrictedIngressSpec{
				PublicPaths: []string{"/records/", "/version"}, RetryGETPaths: []string{"/records/", "/version"}, PrivateReadinessPath: "/readyz",
			}}},
		}
	}
	for _, mutate := range []func(*InfraSpec){
		func(s *InfraSpec) { s.Endpoints[0].URL = "http://pilot.example.test" },
		func(s *InfraSpec) { s.Endpoints[0].Process = "" },
		func(s *InfraSpec) {
			s.Endpoints[0].RestrictedIngress.PublicPaths = []string{"/records/`) || Host(`evil.test"}
		},
		func(s *InfraSpec) { s.Endpoints[0].RestrictedIngress.RetryGETPaths = []string{"/admin"} },
		func(s *InfraSpec) { s.Endpoints[0].RestrictedIngress.PrivateReadinessPath = "/version" },
		func(s *InfraSpec) { s.Processes["web"] = Process{Port: 8080, Health: &HealthSpec{Path: "/healthz"}} },
	} {
		spec := base()
		mutate(spec)
		if ValidateSpec(spec).Valid {
			t.Fatalf("accepted unsafe restricted ingress: %+v", spec.Endpoints[0])
		}
	}
}
