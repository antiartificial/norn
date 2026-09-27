package nomad

import (
	"fmt"
	"maps"
	"strings"

	nomadapi "github.com/hashicorp/nomad/api"
)

type ManagedJobSecretSource interface {
	EnvMap(app string) (map[string]string, error)
}

// StageManagedJobSecretsFromSource loads the app's private secret map, selects
// only file keys named by this job, and refuses a missing key before Nomad IO.
func (c *Client) StageManagedJobSecretsFromSource(app, region string, plan ManagedJobInputRequirements, source ManagedJobSecretSource) error {
	selected, err := loadManagedJobSecrets(app, plan, source)
	if err != nil {
		return err
	}
	return c.StageManagedJobSecretInputs(region, plan, selected)
}

func loadManagedJobSecrets(app string, plan ManagedJobInputRequirements, source ManagedJobSecretSource) (map[string]string, error) {
	if app == "" {
		return nil, fmt.Errorf("managed job secret source is unavailable")
	}
	required := map[string]bool{}
	for _, key := range plan.RequiredKeys {
		if !strings.HasPrefix(key, "norn_") {
			required[key] = true
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	if source == nil {
		return nil, fmt.Errorf("managed job secret source is unavailable")
	}
	all, err := source.EnvMap(app)
	if err != nil {
		return nil, fmt.Errorf("managed job secret source failed for %s", app)
	}
	selected := make(map[string]string, len(required))
	for key := range required {
		if all[key] == "" {
			return nil, fmt.Errorf("managed job secret %s is unavailable", key)
		}
		selected[key] = all[key]
	}
	return selected, nil
}

// StageManagedJobSecretInputs adds only the file keys required by a managed
// revision job. It is idempotent under Nomad CAS and never changes an existing
// value at that job path; a rotated value needs a new deployment revision.
// Values remain private to the caller and Nomad Variable, never the job spec.
func (c *Client) StageManagedJobSecretInputs(region string, plan ManagedJobInputRequirements, values map[string]string) error {
	if c == nil || c.api == nil || region == "" || plan.JobID == "" || plan.VariablePath != DatabaseVariablePath(plan.JobID) {
		return fmt.Errorf("managed secret delivery is incomplete")
	}
	if err := validateManagedJobSecretItems(plan, values); err != nil {
		return err
	}
	if len(values) == 0 {
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

func validateManagedJobSecretItems(plan ManagedJobInputRequirements, values map[string]string) error {
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
	return nil
}
