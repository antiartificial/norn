package nomad

import (
	"fmt"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

func testIntPointer(value int) *int { return &value }

func mustTranslate(t *testing.T, spec *model.InfraSpec, image string, env map[string]string) *nomadapi.Job {
	t.Helper()
	job, err := Translate(spec, image, env)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func mustTranslateForRegion(t *testing.T, spec *model.InfraSpec, image string, env map[string]string, region model.ResolvedRegion) *nomadapi.Job {
	t.Helper()
	job, err := TranslateForRegion(spec, image, env, region)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func mustTranslatePeriodic(t *testing.T, spec *model.InfraSpec, name string, proc model.Process, image string, env map[string]string) *nomadapi.Job {
	t.Helper()
	job, err := TranslatePeriodic(spec, name, proc, image, env)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func mustTranslateBatch(t *testing.T, spec *model.InfraSpec, name string, proc model.Process, image string, env map[string]string, jobID string) *nomadapi.Job {
	t.Helper()
	job, err := TranslateBatch(spec, name, proc, image, env, jobID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestTranslatePreservesContentAddressedImage(t *testing.T) {
	image := "registry.example.test/norn/demo@sha256:" + strings.Repeat("a", 64)
	spec := &model.InfraSpec{
		App:       "demo",
		Processes: map[string]model.Process{"web": {Port: 8080}},
	}
	job := mustTranslate(t, spec, image, nil)
	if len(job.TaskGroups) != 1 || len(job.TaskGroups[0].Tasks) != 1 {
		t.Fatalf("unexpected translated task shape: %+v", job.TaskGroups)
	}
	if got := job.TaskGroups[0].Tasks[0].Config["image"]; got != image {
		t.Fatalf("translated image = %v, want %s", got, image)
	}
}

func TestTranslateRendersJobOwnedVariableFilesWithoutSecretTaskEnv(t *testing.T) {
	const dsn = "pilot:never-in-task-env@tcp(database.example:25060)/pilot"
	const token = "0123456789abcdef0123456789abcdef"
	image := "registry.example.test/norn/hello@sha256:" + strings.Repeat("a", 64)
	spec := &model.InfraSpec{
		App: "hello-norn-mysql",
		Env: map[string]string{
			"MYSQL_DSN_FILE":         "${NOMAD_SECRETS_DIR}/mysql-dsn",
			"MYSQL_CA_FILE":          "${NOMAD_SECRETS_DIR}/mysql-ca.pem",
			"MYSQL_PINNED_IP_FILE":   "${NOMAD_SECRETS_DIR}/mysql-pinned-ip",
			"PILOT_WRITE_TOKEN_FILE": "${NOMAD_SECRETS_DIR}/pilot-write-token",
		},
		Processes: map[string]model.Process{
			"web": {
				Port: 8080,
				NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{
					{Key: "MYSQL_DSN", Destination: "mysql-dsn"},
					{Key: "MYSQL_CA_PEM", Destination: "mysql-ca.pem"},
					{Key: "MYSQL_PINNED_IP", Destination: "mysql-pinned-ip"},
					{Key: "PILOT_WRITE_TOKEN", Destination: "pilot-write-token"},
				}},
			},
		},
	}
	job := mustTranslate(t, spec, image, map[string]string{
		"MYSQL_DSN": dsn, "MYSQL_CA_PEM": "private-ca", "MYSQL_PINNED_IP": "10.20.30.40", "PILOT_WRITE_TOKEN": token,
	})
	task := job.TaskGroups[0].Tasks[0]
	if task.Config["image"] != image {
		t.Fatalf("image=%v", task.Config["image"])
	}
	for _, key := range []string{"MYSQL_DSN", "MYSQL_CA_PEM", "MYSQL_PINNED_IP", "PILOT_WRITE_TOKEN"} {
		if _, found := task.Env[key]; found {
			t.Fatalf("secret key %s was injected into task.Env: %#v", key, task.Env)
		}
	}
	if task.Env["MYSQL_DSN_FILE"] != "${NOMAD_SECRETS_DIR}/mysql-dsn" || task.Env["PILOT_WRITE_TOKEN_FILE"] != "${NOMAD_SECRETS_DIR}/pilot-write-token" {
		t.Fatalf("file-path env=%#v", task.Env)
	}
	if rendered := strings.Join([]string{fmt.Sprint(task.Config), fmt.Sprint(task.Env), fmt.Sprint(task.Templates)}, " "); strings.Contains(rendered, dsn) || strings.Contains(rendered, token) || strings.Contains(rendered, "private-ca") {
		t.Fatalf("resolved secret value leaked into translated job: %s", rendered)
	}
	if len(task.Templates) != 4 {
		t.Fatalf("templates=%d", len(task.Templates))
	}
	for _, template := range task.Templates {
		if template.DestPath == nil || !strings.HasPrefix(*template.DestPath, "secrets/") || template.Perms == nil || *template.Perms != "0400" || template.Uid == nil || *template.Uid != 65532 || template.Gid == nil || *template.Gid != 65532 || template.ChangeMode == nil || *template.ChangeMode != "restart" || template.Envvars == nil || *template.Envvars || template.ErrMissingKey == nil || !*template.ErrMissingKey {
			t.Fatalf("unsafe template=%+v", template)
		}
		if template.EmbeddedTmpl == nil || !strings.Contains(*template.EmbeddedTmpl, `nomadVar "nomad/jobs/hello-norn-mysql"`) {
			t.Fatalf("template did not use derived job-owned path: %+v", template)
		}
	}
}

func TestTranslationRejectsInvalidVariableFileTransportAtRuntime(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Processes: map[string]model.Process{
			"web": {NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "DATABASE_URL", Destination: "../escape"}}}},
		},
	}
	job, err := TranslateForRegion(spec, "orders:test", nil, spec.ResolvedRegions()[0])
	if err == nil || job != nil || !strings.Contains(err.Error(), "destination must be one safe allocation-relative filename") {
		t.Fatalf("job=%+v err=%v, want runtime transport rejection", job, err)
	}
}

func TestTranslationRejectsGeneratedRuntimeInfrastructureWithVariableFilesAtScheduling(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Infrastructure: &model.Infrastructure{
			ObjectStorage: &model.ObjectStorageInfra{Buckets: []model.ObjectStorageBucket{{Name: "orders-assets"}}},
			Kafka:         &model.KafkaInfra{Topics: []string{"orders.created"}},
		},
		Processes: map[string]model.Process{
			"web": {NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "DATABASE_URL", Destination: "database-url"}}}},
		},
	}
	job, err := TranslateForRegion(spec, "orders:test", nil, spec.ResolvedRegions()[0])
	if err == nil || job != nil || !strings.Contains(err.Error(), "generated object-storage and Kafka runtime values") {
		t.Fatalf("job=%+v err=%v, want generated runtime environment rejection", job, err)
	}
}

func TestPeriodicTranslationValidatesDirectProcessCopy(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Processes: map[string]model.Process{
			"nightly": {Schedule: "0 1 * * *"},
		},
	}
	mutated := model.Process{Schedule: "0 2 * * *", NomadVariables: &model.NomadVariableFiles{UID: 0, GID: 0, Files: []model.NomadVariableFile{{Key: "DATABASE_URL", Destination: "db"}}}}
	job, err := TranslatePeriodic(spec, "nightly", mutated, "orders:test", nil)
	if err == nil || job != nil || !strings.Contains(err.Error(), "uid and gid must be explicit positive task identity values") {
		t.Fatalf("job=%+v err=%v, want direct periodic transport rejection", job, err)
	}
}

func TestBatchTranslationRejectsRequestMetadataWithVariableFiles(t *testing.T) {
	proc := model.Process{Function: &model.FunctionSpec{}, NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "DATABASE_URL", Destination: "db"}}}}
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{"function": proc}}
	job, err := TranslateBatch(spec, "function", proc, "orders:test", map[string]string{"DATABASE_URL": "secret", "NORN_REQUEST_BODY": "caller-data"}, "orders-function-1")
	if err == nil || job != nil || !strings.Contains(err.Error(), "function request metadata is unsupported") {
		t.Fatalf("job=%+v err=%v, want request metadata rejection", job, err)
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

	job := mustTranslatePeriodic(t, spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
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

	job := mustTranslatePeriodic(t, spec, "field-harbor-sync-am", proc, "field-harbor:test", nil)
	if job.Periodic == nil || job.Periodic.TimeZone == nil {
		t.Fatal("periodic timezone was not set")
	}
	if got := *job.Periodic.TimeZone; got != "UTC" {
		t.Fatalf("timezone = %q, want UTC", got)
	}
}

func TestTranslatePeriodicDisablesRestartsAndReschedules(t *testing.T) {
	spec := &model.InfraSpec{App: "field-harbor"}
	proc := model.Process{Schedule: "10 8 * * *", Command: "./scripts/sync-and-ingest.sh"}

	assertBatchJobDoesNotRetry(t, mustTranslatePeriodic(t, spec, "field-harbor-sync-am", proc, "field-harbor:test", nil))
}

func TestTranslateBatchDisablesRestartsAndReschedules(t *testing.T) {
	spec := &model.InfraSpec{App: "field-harbor"}
	proc := model.Process{Command: "./functions/daily-capture.sh"}

	assertBatchJobDoesNotRetry(t, mustTranslateBatch(t, spec, "daily-capture", proc, "field-harbor:test", nil, "field-harbor-daily-capture-123"))
}

func assertBatchJobDoesNotRetry(t *testing.T, job *nomadapi.Job) {
	t.Helper()
	if len(job.TaskGroups) != 1 {
		t.Fatalf("task groups = %d, want 1", len(job.TaskGroups))
	}
	group := job.TaskGroups[0]
	if group.RestartPolicy == nil || group.RestartPolicy.Attempts == nil || *group.RestartPolicy.Attempts != 0 || group.RestartPolicy.Mode == nil || *group.RestartPolicy.Mode != "fail" {
		t.Fatalf("restart policy = %+v, want attempts 0 and mode fail", group.RestartPolicy)
	}
	if group.ReschedulePolicy == nil || group.ReschedulePolicy.Attempts == nil || *group.ReschedulePolicy.Attempts != 0 || group.ReschedulePolicy.Unlimited == nil || *group.ReschedulePolicy.Unlimited {
		t.Fatalf("reschedule policy = %+v, want attempts 0 and unlimited false", group.ReschedulePolicy)
	}
}

func TestTranslatePinsServiceAndPeriodicJobsToLogicalNodePool(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Placement: &model.PlacementSpec{NodePool: "app"}, Processes: map[string]model.Process{"web": {Port: 8080}}}
	service := mustTranslate(t, spec, "orders:test", nil)
	if service.NodePool == nil || *service.NodePool != "app" {
		t.Fatalf("service node pool = %v", service.NodePool)
	}
	periodic := mustTranslatePeriodic(t, spec, "digest", model.Process{Schedule: "0 8 * * *"}, "orders:test", nil)
	if periodic.NodePool == nil || *periodic.NodePool != "app" {
		t.Fatalf("periodic node pool = %v", periodic.NodePool)
	}
}

func TestTranslateUsesGroupScopedDistinctHostConstraintForHAService(t *testing.T) {
	spec := &model.InfraSpec{
		App:       "orders",
		Placement: &model.PlacementSpec{NodePool: "app", DistinctHosts: true},
		Processes: map[string]model.Process{"web": {Port: 8080, Scaling: &model.Scaling{Min: 2}, Canary: &model.CanaryConfig{Count: 1}}},
	}
	service := mustTranslate(t, spec, "orders:test", nil)
	if len(service.TaskGroups) != 1 || len(service.TaskGroups[0].Constraints) != 1 {
		t.Fatalf("HA constraints = %+v", service.TaskGroups)
	}
	constraint := service.TaskGroups[0].Constraints[0]
	if constraint.LTarget != "" || constraint.Operand != nomadapi.ConstraintDistinctHosts || constraint.RTarget != "" {
		t.Fatalf("constraint = %+v, want group-level distinct_hosts with no attribute/value", constraint)
	}
	if service.TaskGroups[0].Update == nil || service.TaskGroups[0].Update.Canary == nil || *service.TaskGroups[0].Update.Canary != 1 {
		t.Fatalf("canary = %+v", service.TaskGroups[0].Update)
	}
	// A distinct_hosts group treats its canary as another live allocation. This
	// translation intentionally does not relax hard anti-affinity: rehearsal
	// therefore needs 2 replicas + 1 canary = 3 eligible clients.
	if *service.TaskGroups[0].Count+*service.TaskGroups[0].Update.Canary != 3 {
		t.Fatalf("concurrent HA allocation requirement = %d, want 3", *service.TaskGroups[0].Count+*service.TaskGroups[0].Update.Canary)
	}
	periodic := mustTranslatePeriodic(t, spec, "digest", model.Process{Schedule: "0 8 * * *"}, "orders:test", nil)
	if len(periodic.TaskGroups[0].Constraints) != 0 {
		t.Fatalf("periodic job inherited service HA constraint: %+v", periodic.TaskGroups[0].Constraints)
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
	job := mustTranslateForRegion(t, spec, "orders:test", nil, region)
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
	job := mustTranslateForRegion(t, spec, "worker:test", nil, spec.ResolvedRegions()[0])
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	if !strings.Contains(tags, "norn.region=local") || !strings.Contains(tags, "norn.node-pool=app") || !strings.Contains(tags, "norn.allocation=${NOMAD_ALLOC_ID}") {
		t.Fatalf("placement tags=%s", tags)
	}
	if strings.Contains(tags, "traefik.enable") {
		t.Fatalf("non-endpoint service unexpectedly enabled ingress: %s", tags)
	}
}
