package pipeline

import (
	"testing"

	"norn/v2/api/model"
)

func TestRegionalServiceProcessCountExcludesFunctions(t *testing.T) {
	spec := &model.InfraSpec{App: "function-only", Processes: map[string]model.Process{
		"resize":  {Command: "resize", Function: &model.FunctionSpec{}},
		"cleanup": {Command: "cleanup", Schedule: "0 * * * *"},
	}}
	if got := regionalServiceProcessCount(spec, "local"); got != 0 {
		t.Fatalf("function-only service count = %d", got)
	}
	spec.Processes["web"] = model.Process{Command: "serve"}
	if got := regionalServiceProcessCount(spec, "local"); got != 1 {
		t.Fatalf("service count = %d", got)
	}
}
