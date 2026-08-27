package nomad

import (
	"strings"
	"testing"

	"norn/v2/api/model"
)

func testIntPointer(value int) *int { return &value }

func TestTranslatePreservesContentAddressedImage(t *testing.T) {
	image := "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	spec := &model.InfraSpec{
		App:       "demo",
		Processes: map[string]model.Process{"web": {Port: 8080}},
	}
	job := Translate(spec, image, nil)
	if len(job.TaskGroups) != 1 || len(job.TaskGroups[0].Tasks) != 1 {
		t.Fatalf("unexpected translated task shape: %+v", job.TaskGroups)
	}
	if got := job.TaskGroups[0].Tasks[0].Config["image"]; got != image {
		t.Fatalf("translated image = %v, want %s", got, image)
	}
}

func TestTranslatePeriodicUsesAppTimezone(t *testing.T) {
	spec := &model.InfraSpec{
		App: "field-harbor",
		Env: map[string]string{"TZ": "America/Chicago"},
	}
	proc := model.Process{
		Schedule: "10 8 * * *",
		Command:  "./scripts/sync-and-ingest.sh",
	}

	job := TranslatePeriodic(spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
	if job.Periodic == nil || job.Periodic.TimeZone == nil {
		t.Fatal("periodic timezone was not set")
	}
	if got := *job.Periodic.TimeZone; got != "America/Chicago" {
		t.Fatalf("timezone = %q, want America/Chicago", got)
	}
}

func TestTranslatePeriodicProcessTimezoneOverridesAppTimezone(t *testing.T) {
	spec := &model.InfraSpec{
		App: "field-harbor",
		Env: map[string]string{"TZ": "America/Chicago"},
	}
	proc := model.Process{
		Schedule: "10 8 * * *",
		Timezone: "UTC",
		Command:  "./scripts/sync-and-ingest.sh",
	}

	job := TranslatePeriodic(spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
	if job.Periodic == nil || job.Periodic.TimeZone == nil {
		t.Fatal("periodic timezone was not set")
	}
	if got := *job.Periodic.TimeZone; got != "UTC" {
		t.Fatalf("timezone = %q, want UTC", got)
	}
}

func TestTranslatePinsServiceAndPeriodicJobsToLogicalNodePool(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Placement: &model.PlacementSpec{NodePool: "app"}, Processes: map[string]model.Process{"web": {Port: 8080}}}
	service := Translate(spec, "orders:test", nil)
	if service.NodePool == nil || *service.NodePool != "app" {
		t.Fatalf("service node pool = %v", service.NodePool)
	}
	periodic := TranslatePeriodic(spec, "digest", model.Process{Schedule: "0 8 * * *"}, "orders:test", nil)
	if periodic.NodePool == nil || *periodic.NodePool != "app" {
		t.Fatalf("periodic node pool = %v", periodic.NodePool)
	}
}

func TestTranslateForRegionFiltersPlacementAndAddsIngressTags(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Regions: map[string]model.RegionTarget{
			"ord": {NomadRegion: "us-central", Datacenters: []string{"ord1"}, TrafficWeight: testIntPointer(70)},
			"iad": {NomadRegion: "us-east", Datacenters: []string{"iad1"}, TrafficWeight: testIntPointer(30)},
		},
		PrimaryRegion: "ord",
		Processes: map[string]model.Process{
			"web":    {Port: 8080, Scaling: &model.Scaling{Min: 3}},
			"leader": {Command: "./leader", Singleton: true},
		},
		Endpoints: []model.Endpoint{{URL: "https://orders.example.com"}},
	}
	region := spec.ResolvedRegions()[0] // iad sorts first
	job := TranslateForRegion(spec, "orders:test", nil, region)
	if job.Region == nil || *job.Region != "us-east" {
		t.Fatalf("region=%v", job.Region)
	}
	if len(job.TaskGroups) != 1 || *job.TaskGroups[0].Name != "web" {
		t.Fatalf("groups=%v", job.TaskGroups)
	}
	if got := *job.TaskGroups[0].Count; got != 3 {
		t.Fatalf("count=%d", got)
	}
	if len(job.TaskGroups[0].Networks[0].DynamicPorts) != 1 {
		t.Fatal("endpoint allocations need dynamic host ports")
	}
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	if !strings.Contains(tags, "traefik.enable=true") || !strings.Contains(tags, "Host(`orders.example.com`)") || !strings.Contains(tags, "norn.traffic-weight=30") || !strings.Contains(tags, "norn.region=iad") || !strings.Contains(tags, "norn.allocation=${NOMAD_ALLOC_ID}") {
		t.Fatalf("tags=%s", tags)
	}
}

func TestServicePlacementTagsExistWithoutPublicEndpoint(t *testing.T) {
	spec := &model.InfraSpec{App: "worker", Placement: &model.PlacementSpec{NodePool: "app"}, Processes: map[string]model.Process{"health": {Port: 8080}}}
	job := TranslateForRegion(spec, "worker:test", nil, spec.ResolvedRegions()[0])
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	if !strings.Contains(tags, "norn.region=local") || !strings.Contains(tags, "norn.node-pool=app") || !strings.Contains(tags, "norn.allocation=${NOMAD_ALLOC_ID}") {
		t.Fatalf("placement tags=%s", tags)
	}
	if strings.Contains(tags, "traefik.enable") {
		t.Fatalf("non-endpoint service unexpectedly enabled ingress: %s", tags)
	}
}
