package nomad

import (
	"os"
	"strings"
	"testing"

	"norn/v2/api/model"
)

func TestManagedMySQLStagingDocumentKeepsPilotSeparate(t *testing.T) {
	document, err := os.ReadFile("../../infra/fleet-staging/hello-norn-mysql/infraspec.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := model.ParseInfraSpecDocument(document)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Deploy || spec.App != "hello-norn-mysql-managed" {
		t.Fatalf("unsafe managed pilot identity/deploy state: %s %t", spec.App, spec.Deploy)
	}
	if result := model.ValidateSpec(spec); !result.Valid {
		t.Fatalf("invalid staging spec: %+v", result.Findings)
	}
	job, err := Translate(spec, "reviewed-image@sha256:abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	if *job.ID != spec.App || job.TaskGroups[0].Services[0].Name != "hello-norn-mysql-managed-web" {
		t.Fatalf("service collides with direct pilot: %+v", job.TaskGroups[0].Services[0])
	}
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	if !strings.Contains(tags, "entrypoints=private-readiness") || !strings.Contains(tags, "entrypoints=websecure") || strings.Contains(tags, "entrypoints=web\n") {
		t.Fatalf("invalid managed route: %s", tags)
	}
}

func TestManagedMySQLRestrictedIngressMatchesReviewedPilot(t *testing.T) {
	spec := &model.InfraSpec{
		App:       "hello-norn-mysql-managed",
		Processes: map[string]model.Process{"web": {Port: 8080, Health: &model.HealthSpec{Path: "/readyz"}}},
		Endpoints: []model.Endpoint{{URL: "https://work-staging-hello-norn-mysql.invalid", Process: "web", RestrictedIngress: &model.RestrictedIngressSpec{
			PublicPaths: []string{"/records/", "/version"}, RetryGETPaths: []string{"/records/", "/version"}, PrivateReadinessPath: "/readyz",
		}}},
	}
	if result := model.ValidateSpec(spec); !result.Valid {
		t.Fatalf("invalid managed spec: %+v", result.Findings)
	}
	job, err := Translate(spec, "image@sha256:abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	tags := strings.Join(job.TaskGroups[0].Services[0].Tags, "\n")
	for _, want := range []string{
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0.rule=Host(`work-staging-hello-norn-mysql.invalid`) && (PathPrefix(`/records/`) || Path(`/version`))",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0.entrypoints=websecure",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0.tls=true",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0-read.rule=Host(`work-staging-hello-norn-mysql.invalid`) && Method(`GET`) && (PathPrefix(`/records/`) || Path(`/version`))",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0-read.priority=1000",
		"traefik.http.middlewares.hello-norn-mysql-managed-web-local-0-read-retry.retry.attempts=2",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0-private-readyz.rule=Host(`work-staging-hello-norn-mysql.invalid`) && Path(`/readyz`)",
		"traefik.http.routers.hello-norn-mysql-managed-web-local-0-private-readyz.entrypoints=private-readiness",
	} {
		if !strings.Contains(tags, want) {
			t.Errorf("missing %q in %s", want, tags)
		}
	}
	if strings.Contains(tags, "entrypoints=web\n") || strings.Contains(tags, "PathPrefix(`/readyz`)") {
		t.Fatalf("unexpected public legacy/readiness route: %s", tags)
	}
}
