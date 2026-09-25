package worker

import (
	"fmt"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/nomad"
)

// BuildFunctionInvocationJobPlan binds the durable worker identity to the
// digest of the actual closed Nomad job. Command and resource limits must come
// from the pinned, validated InfraSpec; request and secret bytes never enter
// this public job plan.
func BuildFunctionInvocationJobPlan(input FunctionInvocationEffectInput, command string, cpu, memoryMB int, files ...nomad.FunctionInvocationFileLayout) (FunctionInvocationJobIdentity, *nomadapi.Job, error) {
	// The remote names do not depend on the job digest. Derive them through the
	// same validated constructor before building the job, then replace the
	// placeholder with the digest projected from the complete job.
	placeholder := "sha256:" + strings.Repeat("0", 64)
	names, err := NewFunctionInvocationJobIdentity(input, placeholder)
	if err != nil {
		return FunctionInvocationJobIdentity{}, nil, err
	}
	job, projected, err := nomad.BuildFunctionInvocationJob(nomad.FunctionInvocationJobRequest{
		JobID: names.JobID, OwnerMarker: names.OwnerMarker, VariablePath: names.VariablePath,
		Image: input.ImageReference, Command: command, CPU: cpu, MemoryMB: memoryMB, Files: files,
	})
	if err != nil {
		return FunctionInvocationJobIdentity{}, nil, fmt.Errorf("function invocation job plan is invalid: %w", err)
	}
	identity, err := NewFunctionInvocationJobIdentity(input, projected.Digest)
	if err != nil {
		return FunctionInvocationJobIdentity{}, nil, err
	}
	return identity, job, nil
}
