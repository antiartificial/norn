package nomad

import (
	"fmt"
	"maps"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

// StageManagedJobSecretInputs adds only the file keys required by a managed
// revision job. It is idempotent under Nomad CAS and never changes an existing
// value at that job path; a rotated value needs a new deployment revision.
// Values remain private to the caller and Nomad Variable, never the job spec.
func (c *Client) StageManagedJobSecretInputs(region string, plan ManagedJobInputRequirements, values map[string]string) error {
	if c == nil || c.api == nil || region == "" || plan.JobID == "" || plan.VariablePath != DatabaseVariablePath(plan.JobID) {
		return fmt.Errorf("managed secret delivery is incomplete")
	}
	required := map[string]bool{}
	for _, key := range plan.RequiredKeys {
		if !strings.HasPrefix(key, "norn_") {
			required[key] = true
		}
	}
	if len(values) != len(required) {
		return fmt.Errorf("managed secret delivery has missing or extra keys")
	}
	for key := range required {
		if key == "" || values[key] == "" {
			return fmt.Errorf("managed secret delivery lacks %s", key)
		}
	}
	for key := range values {
		if !required[key] {
			return fmt.Errorf("managed secret delivery has an unexpected key")
		}
	}
	if len(required) == 0 {
		return nil
	}
	current, err := c.peekDatabaseVariable(region, plan.JobID, values)
	if err != nil {
		return err
	}
	if current == nil {
		return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: plan.VariablePath, Items: maps.Clone(values)}, values)
	}
	next := maps.Clone(map[string]string(current.Items))
	changed := false
	for key, value := range values {
		if present, ok := next[key]; ok {
			if present != value {
				return fmt.Errorf("%w for %s: managed secret key differs", ErrDatabaseVariableConflict, plan.JobID)
			}
			continue
		}
		next[key] = value
		changed = true
	}
	if !changed {
		return nil
	}
	return c.writeDatabaseVariable(region, &nomadapi.Variable{Path: plan.VariablePath, Items: next, ModifyIndex: current.ModifyIndex}, values)
}
