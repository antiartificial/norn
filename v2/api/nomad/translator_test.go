package nomad

import (
	"encoding/json"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

func testIntPointer(value int) *int { return &value }

func TestTranslatePreservesEveryVolume(t *testing.T) {
	spec := &model.InfraSpec{
		App:       "demo",
		Processes: map[string]model.Process{"web": {Port: 8080}},
		Volumes: []model.VolumeSpec{
			{Name: "content", Mount: "/var/www/html/wp-content"},
			{Name: "cache", Mount: "/var/cache/demo"},
		},
	}
	jobs := map[string]*nomadapi.Job{
		"service":  Translate(spec, "demo:test", nil),
		"periodic": TranslatePeriodic(spec, "tick", model.Process{Schedule: "0 * * * *", Command: "true"}, "demo:test", nil),
		"batch":    TranslateBatch(spec, "run", model.Process{Command: "true"}, "demo:test", nil, "demo-run-1"),
	}
	for kind, job := range jobs {
		group := job.TaskGroups[0]
		if len(group.Volumes) != 2 || group.Volumes["content"] == nil || group.Volumes["cache"] == nil {
			t.Fatalf("%s volumes = %+v", kind, group.Volumes)
		}
		if got := len(group.Tasks[0].VolumeMounts); got != 2 {
			t.Fatalf("%s mounts = %d, want 2", kind, got)
		}
	}
}

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

// Service, periodic and batch jobs submit explicit log rotation limits, and
// the budget fits each group's ephemeral disk as Nomad validates it.
func TestTranslateSubmitsExplicitTaskLogRotation(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{"web": {Port: 8080}, "worker": {Command: "./work"}}}
	jobs := map[string]*nomadapi.Job{
		"service":  Translate(spec, "orders:test", nil),
		"periodic": TranslatePeriodic(spec, "digest", model.Process{Schedule: "0 8 * * *", Command: "./digest"}, "orders:test", nil),
		"batch":    TranslateBatch(spec, "fn", model.Process{Command: "./fn"}, "orders:test", nil, "orders-fn-1"),
	}
	for kind, job := range jobs {
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"LogConfig":{"MaxFiles":5,"MaxFileSizeMB":10`) {
			t.Fatalf("%s job submits no explicit log rotation: %s", kind, encoded)
		}
		job.Canonicalize()
		for _, group := range job.TaskGroups {
			budget := 0
			for _, task := range group.Tasks {
				if task.LogConfig == nil || *task.LogConfig.MaxFiles != TaskLogMaxFiles || *task.LogConfig.MaxFileSizeMB != TaskLogMaxFileSizeMB || *task.LogConfig.Disabled {
					t.Fatalf("%s task %s log config = %+v", kind, task.Name, task.LogConfig)
				}
				budget += *task.LogConfig.MaxFiles * *task.LogConfig.MaxFileSizeMB
			}
			if group.EphemeralDisk == nil || budget >= *group.EphemeralDisk.SizeMB {
				t.Fatalf("%s group %s log budget %d MB exceeds ephemeral disk %+v", kind, *group.Name, budget, group.EphemeralDisk)
			}
		}
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
	if !strings.Contains(tags, "traefik.enable=true") || !strings.Contains(tags, "Host(`orders.example.com`)") || !strings.Contains(tags, "norn.traffic-weight=30") {
		t.Fatalf("tags=%s", tags)
	}
}
