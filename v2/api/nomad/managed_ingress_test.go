package nomad

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"norn/v2/api/model"
)

func TestManagedDeploymentJobDigestIsStableAcrossReconstruction(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{
		"web": {Port: 8080}, "admin": {Port: 9090}, "worker": {Command: "run-worker"},
	}}
	region := spec.ResolvedRegions()[0]
	var want string
	for i := 0; i < 10; i++ {
		job, err := TranslateForManagedDeployment(spec, "orders:test", nil, region, "deployment-one")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := job.Meta["deploy_ts"]; exists {
			t.Fatal("managed revision retained a wall-clock deploy marker")
		}
		if len(job.TaskGroups) != 3 || *job.TaskGroups[0].Name != "admin" || *job.TaskGroups[1].Name != "web" || *job.TaskGroups[2].Name != "worker" {
			t.Fatalf("managed task group order is unstable: %+v", job.TaskGroups)
		}
		digest, err := DigestDeploymentJob(job)
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && digest != want {
			t.Fatalf("reconstructed managed job digest changed: %s != %s", digest, want)
		}
		want = digest
		time.Sleep(2 * time.Millisecond)
	}
}

func TestManagedDeploymentBackendsAreRevisionSpecificAndDoNotClaimPublicHost(t *testing.T) {
	spec := &model.InfraSpec{
		App: "orders",
		Processes: map[string]model.Process{
			"web": {Port: 8080, NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532,
				Files: []model.NomadVariableFile{{Key: "API_TOKEN", Destination: "api-token"}}}},
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
	if *first.ID == *second.ID || *first.ID == spec.App || *second.ID == spec.App {
		t.Fatalf("deployment revisions share an app or Nomad job identity: %q %q", *first.ID, *second.ID)
	}
	jobID, err := ManagedDeploymentJobID("orders", region.Name, "deployment-one")
	if err != nil || *first.ID != jobID {
		t.Fatalf("first revision job identity is unstable: %q, %v", *first.ID, err)
	}
	if len(first.TaskGroups[0].Tasks[0].Templates) != 1 ||
		!strings.Contains(*first.TaskGroups[0].Tasks[0].Templates[0].EmbeddedTmpl, "nomad/jobs/"+jobID) {
		t.Fatal("managed revision did not bind its variable template to its own job")
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

func TestManagedJobInputPlanNamesExactRevisionScopedKeys(t *testing.T) {
	spec := &model.InfraSpec{App: "orders", Processes: map[string]model.Process{
		"web": {Port: 8080, NomadVariables: &model.NomadVariableFiles{UID: 65532, GID: 65532, Files: []model.NomadVariableFile{{Key: "API_TOKEN", Destination: "token"}}}},
	}, Databases: []model.DatabaseRequirement{{Name: "primary", Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}}}
	region := spec.ResolvedRegions()[0]
	if _, err := PlanManagedJobInputs(spec, region, "deployment-one", 0); err == nil {
		t.Fatal("unstaged runtime database accepted")
	}
	plan, err := PlanManagedJobInputs(spec, region, "deployment-one", 7)
	if err != nil {
		t.Fatal(err)
	}
	if plan.VariablePath != DatabaseVariablePath(plan.JobID) || !reflect.DeepEqual(plan.RequiredKeys, []string{
		"API_TOKEN", "norn_rev7_db_target_primary", "norn_rev7_db_url_primary",
	}) || !reflect.DeepEqual(plan.RuntimeDatabaseNames, []string{"primary"}) {
		t.Fatalf("incorrect managed input plan: %+v", plan)
	}
	job, err := TranslateManagedDeploymentForRegionAt(spec, "orders:test", nil, region, "deployment-one", 7)
	if err != nil || ValidateManagedJobInputPlan(job, plan) != nil {
		t.Fatalf("complete managed input plan rejected: %v", err)
	}
	missing := plan
	missing.RequiredKeys = []string{DatabaseRevisionItemKey("primary", 7), DatabaseRevisionTargetKey("primary", 7)}
	if err := ValidateManagedJobInputPlan(job, missing); err == nil {
		t.Fatal("template key missing from signed plan was accepted")
	}
	missing = plan
	missing.RuntimeDatabaseNames = nil
	if err := ValidateManagedJobInputPlan(job, missing); err == nil {
		t.Fatal("database template without target expectation was accepted")
	}
	spec.Processes["web"] = model.Process{Port: 8080, NomadVariables: &model.NomadVariableFiles{Files: []model.NomadVariableFile{{Key: "NORN_REV7_DB_URL_PRIMARY"}}}}
	if _, err := PlanManagedJobInputs(spec, region, "deployment-one", 7); err == nil {
		t.Fatal("reserved variable key accepted")
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

func TestManagedDatabaseJobRequiresStagedRevisionAndJobScopedVariable(t *testing.T) {
	spec := &model.InfraSpec{SchemaVersion: model.AppSchemaV2, App: "orders",
		Processes: map[string]model.Process{"web": {Port: 8080}},
		Databases: []model.DatabaseRequirement{{Name: "primary", Purpose: "application", Capabilities: []string{"runtime"},
			Runtime: &model.DatabaseRuntime{Env: "DATABASE_URL"}}},
	}
	region := spec.ResolvedRegions()[0]
	if _, err := TranslateForManagedDeployment(spec, "orders:test", nil, region, "deployment-one"); err == nil {
		t.Fatal("managed database job accepted an unstaged revision")
	}
	job, err := TranslateManagedDeploymentForRegionAt(spec, "orders:test", nil, region, "deployment-one", 7)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := ManagedDeploymentJobID(spec.App, region.Name, "deployment-one")
	if err != nil || *job.ID != jobID {
		t.Fatalf("managed database job identity=%q err=%v", *job.ID, err)
	}
	templates := job.TaskGroups[0].Tasks[0].Templates
	if len(templates) != 1 || !strings.Contains(*templates[0].EmbeddedTmpl, "nomad/jobs/"+jobID) ||
		!strings.Contains(*templates[0].EmbeddedTmpl, "norn_rev7_db_url_primary") ||
		strings.Contains(*templates[0].EmbeddedTmpl, "nomad/jobs/orders\"") {
		t.Fatalf("database template did not bind staged revision and job: %+v", templates)
	}
}
