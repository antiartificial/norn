package worker

import (
	"strings"
	"testing"

	"norn/v2/api/nomad"
)

func TestBuildFunctionInvocationJobPlanBindsActualJobDigest(t *testing.T) {
	input := testFunctionJobPlanInput()
	identity, job, err := BuildFunctionInvocationJobPlan(input, "./function", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := nomad.ProjectFunctionInvocationJob(job)
	if err != nil {
		t.Fatal(err)
	}
	if identity.JobSpecDigest != projected.Digest || identity.JobID != *job.ID || identity.OwnerMarker != job.Meta["norn.function-invocation.owner"] {
		t.Fatalf("identity does not bind projected job: %+v, %+v", identity, projected)
	}
	changed, _, err := BuildFunctionInvocationJobPlan(input, "./different", 250, 192)
	if err != nil {
		t.Fatal(err)
	}
	if changed.JobSpecDigest == identity.JobSpecDigest || changed.JobID != identity.JobID {
		t.Fatalf("command change did not change only public job digest: %s, %s", changed.JobSpecDigest, identity.JobSpecDigest)
	}
}

func TestBuildFunctionInvocationJobPlanRejectsMutableImage(t *testing.T) {
	input := testFunctionJobPlanInput()
	input.ImageReference = "example/function:latest"
	_, _, err := BuildFunctionInvocationJobPlan(input, "./function", 250, 192)
	if err == nil || !strings.Contains(err.Error(), "digest-pinned") {
		t.Fatalf("mutable image accepted: %v", err)
	}
}

func testFunctionJobPlanInput() FunctionInvocationEffectInput {
	return FunctionInvocationEffectInput{
		Authority: "control.example", OperationID: "operation-7", App: "widgets", Process: "resize",
		SpecDigest: "sha256:" + strings.Repeat("a", 64), ImageReference: "registry.example/widgets@sha256:" + strings.Repeat("b", 64),
		DatabaseTarget: "widgets-primary", DatabaseRevision: "14", PrivateRecordID: "operation-7",
		PrivateMaterialDigest: "sha256:" + strings.Repeat("c", 64), PrivateKeyID: "function-kek-2026-09",
	}
}
