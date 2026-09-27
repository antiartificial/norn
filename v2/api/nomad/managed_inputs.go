package nomad

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

// ManagedJobInputRequirements names the private Nomad Variable keys a
// revision job's templates read. It contains no connection or secret values.
type ManagedJobInputRequirements struct {
	JobID                string   `json:"jobId"`
	VariablePath         string   `json:"variablePath"`
	DatabaseRevision     int64    `json:"databaseRevision"`
	RequiredKeys         []string `json:"requiredKeys"`
	RuntimeDatabaseNames []string `json:"runtimeDatabaseNames"`
}

var managedTemplateKeyPattern = regexp.MustCompile(`\.([A-Za-z0-9_]+)\.Value`)

// ValidateManagedJobInputPlan binds a signed input plan to the exact Nomad
// templates being submitted. A template cannot read an unlisted key or a
// different Variable path; staged database keys require an expected target.
func ValidateManagedJobInputPlan(job *nomadapi.Job, plan ManagedJobInputRequirements) error {
	if job == nil || job.ID == nil || *job.ID != plan.JobID || plan.VariablePath != DatabaseVariablePath(plan.JobID) {
		return fmt.Errorf("managed job input plan has a different job identity")
	}
	planned := make(map[string]bool, len(plan.RequiredKeys))
	for _, key := range plan.RequiredKeys {
		if key == "" || planned[key] {
			return fmt.Errorf("managed job input plan has an invalid key")
		}
		planned[key] = true
	}
	allowedDatabaseKeys := map[string]bool{}
	for _, name := range plan.RuntimeDatabaseNames {
		if name == "" || plan.DatabaseRevision < 1 || !planned[DatabaseRevisionTargetKey(name, plan.DatabaseRevision)] {
			return fmt.Errorf("managed job input plan lacks a database target")
		}
		allowedDatabaseKeys[DatabaseRevisionItemKey(name, plan.DatabaseRevision)] = true
		for _, field := range []string{"host", "user", "password", "name"} {
			allowedDatabaseKeys[stagedKey(DatabaseComponentItemKey(name, field), plan.DatabaseRevision)] = true
		}
		for _, field := range []string{"ca", "client_cert", "client_key"} {
			allowedDatabaseKeys[stagedKey(DatabaseTLSItemKey(name, field), plan.DatabaseRevision)] = true
		}
	}
	read := map[string]bool{}
	for _, group := range job.TaskGroups {
		if group == nil {
			return fmt.Errorf("managed job has a nil task group")
		}
		for _, task := range group.Tasks {
			if task == nil {
				return fmt.Errorf("managed job has a nil task")
			}
			for _, tmpl := range task.Templates {
				if tmpl == nil || tmpl.EmbeddedTmpl == nil {
					continue
				}
				data := *tmpl.EmbeddedTmpl
				if !strings.Contains(data, "nomadVar") {
					continue
				}
				if !strings.Contains(data, fmt.Sprintf("nomadVar %q", plan.VariablePath)) ||
					strings.Count(data, "nomadVar") != 1 {
					return fmt.Errorf("managed job template reads another Variable path")
				}
				matches := managedTemplateKeyPattern.FindAllStringSubmatch(data, -1)
				if len(matches) == 0 {
					return fmt.Errorf("managed job template has no recognizable Variable key")
				}
				for _, match := range matches {
					key := match[1]
					if !planned[key] || (strings.HasPrefix(key, "norn_rev") && !allowedDatabaseKeys[key]) {
						return fmt.Errorf("managed job template reads an unverified Variable key")
					}
					read[key] = true
				}
			}
		}
	}
	for key := range planned {
		if read[key] {
			continue
		}
		allowedTarget := false
		for _, name := range plan.RuntimeDatabaseNames {
			if key == DatabaseRevisionTargetKey(name, plan.DatabaseRevision) {
				allowedTarget = true
				break
			}
		}
		if !allowedTarget {
			return fmt.Errorf("managed job input plan includes a key no template reads")
		}
	}
	return nil
}

// CheckManagedJobInputs confirms the private job variable contains every
// planned value and the accepted identity of each staged database target.
// No variable value is included in an error.
func (c *Client) CheckManagedJobInputs(ctx context.Context, region string, plan ManagedJobInputRequirements, expectedTargets map[string]string) error {
	if c == nil || c.api == nil || region == "" || plan.JobID == "" || plan.VariablePath != DatabaseVariablePath(plan.JobID) ||
		(len(plan.RuntimeDatabaseNames) > 0 && plan.DatabaseRevision < 1) {
		return fmt.Errorf("managed job input check is incomplete")
	}
	for _, name := range plan.RuntimeDatabaseNames {
		if expectedTargets[name] == "" {
			return fmt.Errorf("managed job database target expectation is missing for %s", name)
		}
	}
	if len(plan.RequiredKeys) == 0 && len(plan.RuntimeDatabaseNames) == 0 {
		return nil
	}
	variable, _, err := c.api.Variables().Read(plan.VariablePath, (&nomadapi.QueryOptions{Region: region}).WithContext(ctx))
	if err != nil || variable == nil || variable.Path != plan.VariablePath {
		return fmt.Errorf("managed job inputs are unavailable for %s", plan.JobID)
	}
	for _, key := range plan.RequiredKeys {
		if key == "" || variable.Items[key] == "" {
			return fmt.Errorf("managed job input %s is missing for %s", key, plan.JobID)
		}
	}
	for _, name := range plan.RuntimeDatabaseNames {
		if variable.Items[DatabaseRevisionTargetKey(name, plan.DatabaseRevision)] != expectedTargets[name] {
			return fmt.Errorf("managed job database target differs for %s", name)
		}
	}
	return nil
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
