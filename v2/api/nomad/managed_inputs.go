package nomad

import (
	"fmt"
	"sort"
	"strings"

	"norn/v2/api/model"
)

// ManagedJobInputRequirements names the private Nomad Variable keys a
// revision job's templates read. It contains no connection or secret values.
type ManagedJobInputRequirements struct {
	JobID                string
	VariablePath         string
	DatabaseRevision     int64
	RequiredKeys         []string
	RuntimeDatabaseNames []string
}

func PlanManagedJobInputs(spec *model.InfraSpec, region model.ResolvedRegion, deploymentID string, databaseRevision int64) (ManagedJobInputRequirements, error) {
	if spec == nil {
		return ManagedJobInputRequirements{}, fmt.Errorf("managed job spec is missing")
	}
	jobID, err := ManagedDeploymentJobID(spec.App, region.Name, deploymentID)
	if err != nil {
		return ManagedJobInputRequirements{}, err
	}
	if HasRuntimeDatabases(spec) && databaseRevision < 1 {
		return ManagedJobInputRequirements{}, fmt.Errorf("managed deployment has no staged database revision")
	}
	plan := ManagedJobInputRequirements{JobID: jobID, VariablePath: DatabaseVariablePath(jobID), DatabaseRevision: databaseRevision}
	keys := map[string]bool{}
	for process, definition := range spec.Processes {
		if definition.Schedule != "" || definition.Function != nil || !spec.ProcessRunsInRegion(definition, region.Name) || definition.NomadVariables == nil {
			continue
		}
		for _, file := range definition.NomadVariables.Files {
			if file.Key == "" || strings.HasPrefix(strings.ToLower(file.Key), "norn_") {
				return ManagedJobInputRequirements{}, fmt.Errorf("managed process %s variable key conflicts with reserved delivery", process)
			}
			keys[file.Key] = true
		}
	}
	for _, requirement := range spec.Databases {
		runtime := requirement.Runtime
		if runtime == nil {
			continue
		}
		plan.RuntimeDatabaseNames = append(plan.RuntimeDatabaseNames, requirement.Name)
		keys[DatabaseRevisionTargetKey(requirement.Name, databaseRevision)] = true
		if runtime.Env != "" || runtime.FileEnv != "" {
			keys[DatabaseRevisionItemKey(requirement.Name, databaseRevision)] = true
		}
		if runtime.Components != nil {
			for _, field := range []string{"host", "user", "password", "name"} {
				keys[stagedKey(DatabaseComponentItemKey(requirement.Name, field), databaseRevision)] = true
			}
		}
		if runtime.TLS != nil {
			for _, field := range []struct{ env, material string }{
				{runtime.TLS.CAFileEnv, "ca"}, {runtime.TLS.ClientCertFileEnv, "client_cert"}, {runtime.TLS.ClientKeyFileEnv, "client_key"},
			} {
				if field.env != "" {
					keys[stagedKey(DatabaseTLSItemKey(requirement.Name, field.material), databaseRevision)] = true
				}
			}
		}
	}
	for key := range keys {
		plan.RequiredKeys = append(plan.RequiredKeys, key)
	}
	sort.Strings(plan.RequiredKeys)
	sort.Strings(plan.RuntimeDatabaseNames)
	return plan, nil
}
