package nomad

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	nomadapi "github.com/hashicorp/nomad/api"
)

func functionInvocationJobRequest() FunctionInvocationJobRequest {
	return FunctionInvocationJobRequest{JobID: "norn-fn-" + strings.Repeat("a", 40), OwnerMarker: "norn.function-invoke/op-1", VariablePath: "nomad/jobs/norn-fn-" + strings.Repeat("a", 40) + "/invoke", Image: "registry.example/function@sha256:" + strings.Repeat("b", 64), Command: "./function", CPU: 250, MemoryMB: 192}
}

func TestProjectFunctionInvocationJobAcceptsKnownNomadReadbackDefaults(t *testing.T) {
	job, want, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	defaultPool := "default"
	preventReschedule := false
	zero := 0
	job.NodePool = &defaultPool
	job.Update = nomadapi.DefaultUpdateStrategy()
	job.TaskGroups[0].PreventRescheduleOnLost = &preventReschedule
	task := job.TaskGroups[0].Tasks[0]
	task.Identity = &nomadapi.WorkloadIdentity{Name: "default", Audience: []string{"nomadproject.io"}}
	task.Resources.MemoryMaxMB, task.Resources.SecretsMB, task.Resources.IOPS = &zero, &zero, &zero
	task.Config["args"] = []interface{}{"-c", "./function"}
	got, err := ProjectFunctionInvocationJob(job)
	if err != nil || got != want {
		t.Fatalf("project = %+v, %v; want %+v", got, err, want)
	}
}

func TestBuildFunctionInvocationJobClosedDialectAndStableDigest(t *testing.T) {
	request := functionInvocationJobRequest()
	request.Files = functionInvocationJobTestFiles()
	job, want, err := BuildFunctionInvocationJob(request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ProjectFunctionInvocationJob(job)
	if err != nil || got != want {
		t.Fatalf("project = %+v, %v; want %+v", got, err, want)
	}
	group, task := job.TaskGroups[0], job.TaskGroups[0].Tasks[0]
	if *job.Type != "batch" || len(job.TaskGroups) != 1 || len(group.Tasks) != 1 || task.Env != nil || len(task.Config) != 3 || strings.Contains(strings.Join(mapValues(task.Config), " "), "private") {
		t.Fatalf("job escaped closed dialect: %#v", job)
	}
	if got := *task.Templates[0].EmbeddedTmpl; !strings.Contains(got, request.VariablePath) || !strings.Contains(got, "base64Decode | parseJSON") || !strings.Contains(got, "NORN_REQUEST_BODY=") || strings.Contains(got, "NORN_FUNCTION_JOB_PRIVATE") || task.Templates[0].Envvars == nil || !*task.Templates[0].Envvars {
		t.Fatalf("template = %q", got)
	}
	if len(task.Templates) != 3 || !strings.Contains(*task.Templates[0].EmbeddedTmpl, "MYSQL_SSL_CA") || !strings.Contains(*task.Templates[1].EmbeddedTmpl, `index $p.files "db_tls_ca_primary"`) || *task.Templates[1].Perms != "0400" {
		t.Fatalf("private file templates = %#v", task.Templates)
	}
	encoded, err := json.Marshal(job)
	if err != nil || strings.Contains(string(encoded), "private-ca-canary") {
		t.Fatalf("job JSON contains private material: %v", err)
	}
}

func TestBuildFunctionInvocationJobRejectsNonCanonicalFileLayout(t *testing.T) {
	request := functionInvocationJobRequest()
	request.Files = functionInvocationJobTestFiles()
	request.Files[0], request.Files[1] = request.Files[1], request.Files[0]
	if _, _, err := BuildFunctionInvocationJob(request); err != ErrFunctionInvocationJobRequest {
		t.Fatalf("unsorted files error = %v", err)
	}
	request = functionInvocationJobRequest()
	request.Files = functionInvocationJobTestFiles()
	request.Files[1].Env = request.Files[0].Env
	if _, _, err := BuildFunctionInvocationJob(request); err != ErrFunctionInvocationJobRequest {
		t.Fatalf("duplicate env error = %v", err)
	}
}

func functionInvocationJobTestFiles() []FunctionInvocationFileLayout {
	return []FunctionInvocationFileLayout{{Key: "db_tls_ca_primary", Env: "MYSQL_SSL_CA"}, {Key: "db_url_primary", Env: "DATABASE_URL_FILE"}}
}

func TestProjectFunctionInvocationJobRejectsUnsupportedMutationSurface(t *testing.T) {
	job, _, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	job.TaskGroups[0].Services = []*nomadapi.Service{{Name: "unexpected"}}
	if _, err := ProjectFunctionInvocationJob(job); !errors.Is(err, ErrFunctionInvocationJobDialect) {
		t.Fatalf("project error = %v", err)
	}
}

func TestProjectFunctionInvocationJobRejectsPrivateEnvOrConfig(t *testing.T) {
	job, _, err := BuildFunctionInvocationJob(functionInvocationJobRequest())
	if err != nil {
		t.Fatal(err)
	}
	job.TaskGroups[0].Tasks[0].Env = map[string]string{"PRIVATE": "secret"}
	if _, err := ProjectFunctionInvocationJob(job); !errors.Is(err, ErrFunctionInvocationJobDialect) {
		t.Fatalf("env error = %v", err)
	}
	job, _, _ = BuildFunctionInvocationJob(functionInvocationJobRequest())
	job.TaskGroups[0].Tasks[0].Config["private"] = "secret"
	if _, err := ProjectFunctionInvocationJob(job); !errors.Is(err, ErrFunctionInvocationJobDialect) {
		t.Fatalf("config error = %v", err)
	}
}

func TestBuildFunctionInvocationJobRejectsMutableImage(t *testing.T) {
	request := functionInvocationJobRequest()
	request.Image = "registry.example/function:latest"
	if _, _, err := BuildFunctionInvocationJob(request); err != ErrFunctionInvocationJobRequest {
		t.Fatalf("build error = %v", err)
	}
}

func mapValues(values map[string]interface{}) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
