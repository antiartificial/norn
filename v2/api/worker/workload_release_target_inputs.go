package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
	"norn/v2/api/pipeline"
	"norn/v2/api/secrets"
	"norn/v2/api/store"
)

// WorkloadReleasePrivateInputOpener returns the authenticated encrypted SOPS
// snapshot bound to the accepted operation and release plan.
type WorkloadReleasePrivateInputOpener interface {
	OpenPrivateWorkloadRelease(context.Context, model.Operation, *store.WorkloadReleaseInputKeyRing) ([]byte, error)
}

// WorkloadReleaseTargetInputs combines a deterministic target job with only
// the application secrets that its managed Nomad input plan names.
type WorkloadReleaseTargetInputs struct {
	JobPlan WorkloadReleaseTargetJobPlan
	Secrets map[string]string
}

// PrepareWorkloadReleaseTargetInputs reopens the accepted encrypted SOPS
// snapshot, decrypts it from memory, and selects only inputs required by the
// plan-derived target job. The returned secret map must only be passed to the
// exact target job's private Nomad Variable staging path.
func PrepareWorkloadReleaseTargetInputs(ctx context.Context, operation model.Operation, plan pipeline.WorkloadReleasePlan,
	opener WorkloadReleasePrivateInputOpener, keys *store.WorkloadReleaseInputKeyRing) (WorkloadReleaseTargetInputs, error) {
	return prepareWorkloadReleaseTargetInputs(ctx, operation, plan, opener, keys, secrets.DecryptSOPSSnapshot)
}

type workloadReleaseSnapshotDecryptor func([]byte) (map[string]string, error)

func prepareWorkloadReleaseTargetInputs(ctx context.Context, operation model.Operation, plan pipeline.WorkloadReleasePlan,
	opener WorkloadReleasePrivateInputOpener, keys *store.WorkloadReleaseInputKeyRing, decrypt workloadReleaseSnapshotDecryptor) (WorkloadReleaseTargetInputs, error) {
	invalid := func(err error) (WorkloadReleaseTargetInputs, error) {
		return WorkloadReleaseTargetInputs{}, err
	}
	if operation.Kind != "app.workload-release" || operation.ID == "" || operation.App == "" || operation.App != plan.Intent.App ||
		opener == nil || keys == nil || decrypt == nil {
		return invalid(fmt.Errorf("workload release private inputs are unavailable"))
	}
	if err := plan.Validate(); err != nil {
		return invalid(fmt.Errorf("workload release plan is invalid: %w", err))
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		return invalid(fmt.Errorf("workload release plan cannot be encoded"))
	}
	acceptedPlan, ok := operation.Payload["workloadReleasePlan"].(string)
	if !ok || !bytes.Equal(encodedPlan, []byte(acceptedPlan)) {
		return invalid(fmt.Errorf("workload release operation does not contain the exact accepted plan"))
	}
	materialBytes, err := opener.OpenPrivateWorkloadRelease(ctx, operation, keys)
	if err != nil {
		return invalid(fmt.Errorf("accepted workload release input snapshot is unavailable"))
	}
	defer clear(materialBytes)
	spec, encryptedSOPSSnapshot, err := store.DecodeWorkloadReleasePinnedMaterial(materialBytes, plan.Intent.App, plan.Intent.TargetSpecDigest)
	if err != nil {
		return invalid(fmt.Errorf("accepted workload release source and input snapshot are unavailable"))
	}
	defer clear(encryptedSOPSSnapshot)
	defer clearWorkloadReleaseSourceSpec(spec)
	jobPlan, err := BuildWorkloadReleaseTargetJobPlan(plan, spec)
	if err != nil {
		return invalid(err)
	}
	values, err := decrypt(encryptedSOPSSnapshot)
	if err != nil {
		return invalid(fmt.Errorf("accepted workload release input snapshot could not be decrypted"))
	}
	defer clear(values)
	selected, err := nomad.SelectManagedJobSecretInputs(*jobPlan.Inputs, values)
	if err != nil {
		return invalid(fmt.Errorf("accepted workload release input snapshot lacks a target job secret"))
	}
	return WorkloadReleaseTargetInputs{JobPlan: jobPlan, Secrets: selected}, nil
}

func clearWorkloadReleaseSourceSpec(spec *model.InfraSpec) {
	if spec == nil {
		return
	}
	for key := range spec.Env {
		spec.Env[key] = ""
		delete(spec.Env, key)
	}
	for processName, process := range spec.Processes {
		for key := range process.Env {
			process.Env[key] = ""
			delete(process.Env, key)
		}
		spec.Processes[processName] = process
	}
}
