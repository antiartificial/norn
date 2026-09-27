package nomad

import (
	"strings"
	"testing"

	"norn/v2/api/model"
)

func TestManagedDeploymentBackendsAreRevisionSpecificAndDoNotClaimPublicHost(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Processes: map[string]model.Process{
			"web": {Port: 8080},
		},
		Endpoints: []model.Endpoint{{URL: "https://orders.example.com"}},
	}
	region := spec.ResolvedRegions()[0]
	first, err := TranslateForManagedDeployment(spec, "orders:test", nil, region, "deployment-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := TranslateForManagedDeployment(spec, "orders:test", nil, region, "deployment-two")
	if err != nil {
		t.Fatal(err)
	}
	if *first.ID != *second.ID || *first.ID != spec.App {
		t.Fatalf("managed translation changed stable Nomad job identity: %q %q", *first.ID, *second.ID)
	}
	serviceOne := first.TaskGroups[0].Services[0]
	serviceTwo := second.TaskGroups[0].Services[0]
	if serviceOne.Name == serviceTwo.Name || !strings.HasPrefix(serviceOne.Name, "norn-") {
		t.Fatalf("deployment revisions share a Consul backend: %q %q", serviceOne.Name, serviceTwo.Name)
	}
	for _, service := range []string{strings.Join(serviceOne.Tags, "\n"), strings.Join(serviceTwo.Tags, "\n")} {
		if strings.Contains(service, "orders.example.com") || strings.Contains(service, "norn.traffic-weight") || !strings.Contains(service, ".norn.invalid") || !strings.Contains(service, "traefik.enable=true") {
			t.Fatalf("managed backend tags exposed or misrepresented public traffic: %s", service)
		}
	}
	repeated, err := ManagedBackendServiceName("orders", "web", region.Name, "deployment-one")
	if err != nil || repeated != serviceOne.Name {
		t.Fatalf("backend identity is not deterministic: %q, %v", repeated, err)
	}
}

func TestManagedDeploymentWithoutRegionalEndpointStaysPrivate(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{"web": {Port: 8080}},
		Endpoints: []model.Endpoint{{URL: "https://orders.example.com", Region: "other"}}}
	job, err := TranslateForManagedDeployment(spec, "orders:test", nil, spec.ResolvedRegions()[0], "deployment-one")
	if err != nil {
		t.Fatal(err)
	}
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	if strings.Contains(tags, "traefik.enable=true") || strings.Contains(tags, "orders.example.com") {
		t.Fatalf("unmatched regional endpoint exposed service: %s", tags)
	}
	if _, err := ManagedBackendServiceName("orders", "web", "local", ""); err == nil {
		t.Fatal("empty deployment identity was accepted")
	}
}
