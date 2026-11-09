package worker

import (
	"norn/v2/api/model"
	"norn/v2/api/pipeline"
)

// WorkloadReleaseTargetJobPlan remains the worker-facing name while the
// deterministic builder lives beside the release contract for reuse at admission.
type WorkloadReleaseTargetJobPlan = pipeline.WorkloadReleaseTargetJobPlan

// BuildWorkloadReleaseTargetJobPlan preserves the worker API and delegates to
// the shared admission/execution translator.
func BuildWorkloadReleaseTargetJobPlan(plan pipeline.WorkloadReleasePlan, spec *model.InfraSpec) (WorkloadReleaseTargetJobPlan, error) {
	return pipeline.BuildWorkloadReleaseTargetJobPlan(plan, spec)
}
