package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

func TestBindDeploymentJobProvenanceBindsExactSignedDatabaseTargets(t *testing.T) {
	encoded := `{"schema":"norn.database-targets/v1","profileId":"mini","catalogRevision":29,"targets":[{"name":"primary","target":{"serviceId":"mysql","serviceGeneration":1,"bindingId":"wordpress","bindingGeneration":2,"engine":"mysql","database":"wordpress","role":"wordpress"}}]}`
	job := &nomadapi.Job{Meta: map[string]string{"norn_region": "local"}}
	payload := map[string]interface{}{databaseTargetsPayloadKey: encoded}
	if err := bindDeploymentJobProvenance(job, "deployment-1", "sha256:"+strings.Repeat("a", 64), payload); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(recordedTargetSetSchema + "\x00" + encoded))
	want := map[string]string{
		nomad.DeploymentIDMeta:            "deployment-1",
		nomad.SpecDigestMeta:              "sha256:" + strings.Repeat("a", 64),
		nomad.DatabaseBindingSchemaMeta:   recordedTargetSetSchema,
		nomad.DatabaseBindingSHA256Meta:   hex.EncodeToString(digest[:]),
		nomad.DatabaseCatalogRevisionMeta: "29",
	}
	for key, value := range want {
		if job.Meta[key] != value {
			t.Fatalf("job meta %s=%q, want %q", key, job.Meta[key], value)
		}
	}
	if job.Meta["norn_region"] != "local" {
		t.Fatal("existing Nomad metadata was replaced")
	}
}

func TestBindDeploymentJobProvenanceRejectsMalformedOrConflictingInput(t *testing.T) {
	valid := `{"schema":"norn.database-targets/v1","profileId":"mini","catalogRevision":29,"targets":[]}`
	tests := []struct {
		name    string
		job     *nomadapi.Job
		deploy  string
		digest  string
		payload map[string]interface{}
	}{
		{name: "missing deployment", job: &nomadapi.Job{}, digest: "sha256:" + strings.Repeat("a", 64)},
		{name: "missing spec digest", job: &nomadapi.Job{}, deploy: "deployment-1"},
		{name: "malformed targets", job: &nomadapi.Job{}, deploy: "deployment-1", digest: "sha256:" + strings.Repeat("a", 64), payload: map[string]interface{}{databaseTargetsPayloadKey: `{}`}},
		{name: "metadata conflict", job: &nomadapi.Job{Meta: map[string]string{nomad.DeploymentIDMeta: "other"}}, deploy: "deployment-1", digest: "sha256:" + strings.Repeat("a", 64), payload: map[string]interface{}{databaseTargetsPayloadKey: valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := bindDeploymentJobProvenance(test.job, test.deploy, test.digest, test.payload); err == nil {
				t.Fatal("unsafe provenance input was accepted")
			}
		})
	}
}

func TestBindDeploymentJobProvenanceMarksNoDatabaseBinding(t *testing.T) {
	job := &nomadapi.Job{}
	if err := bindDeploymentJobProvenance(job, "deployment-1", "sha256:"+strings.Repeat("b", 64), nil); err != nil {
		t.Fatal(err)
	}
	if job.Meta[nomad.DatabaseBindingSchemaMeta] != nomadNoDatabaseBindingSchema || job.Meta[nomad.DatabaseCatalogRevisionMeta] != "0" || len(job.Meta[nomad.DatabaseBindingSHA256Meta]) != 64 {
		t.Fatalf("no-database provenance=%v", job.Meta)
	}
}
