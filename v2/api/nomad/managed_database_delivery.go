package nomad

import (
	"fmt"
	"strings"
)

// StageManagedJobDatabaseInputs accepts current-keyed connection material
// from a probed resolver and stages exactly the signed plan's revision. The
// target items must match the accepted, secret-free identities before IO.
func (c *Client) StageManagedJobDatabaseInputs(region string, plan ManagedJobInputRequirements, items, expectedTargets map[string]string) error {
	if c == nil || c.api == nil || region == "" || plan.JobID == "" || plan.VariablePath != DatabaseVariablePath(plan.JobID) {
		return fmt.Errorf("managed database delivery is incomplete")
	}
	if len(plan.RuntimeDatabaseNames) == 0 {
		if len(items) != 0 || len(expectedTargets) != 0 {
			return fmt.Errorf("managed job has unexpected database material")
		}
		return nil
	}
	if plan.DatabaseRevision < 1 || len(expectedTargets) != len(plan.RuntimeDatabaseNames) {
		return fmt.Errorf("managed database delivery has no accepted revision or targets")
	}
	stagedPrefix := revisionPrefix(plan.DatabaseRevision)
	want := make(map[string]bool)
	for _, key := range plan.RequiredKeys {
		if !strings.HasPrefix(key, "norn_") {
			continue
		}
		if !strings.HasPrefix(key, stagedPrefix) {
			return fmt.Errorf("managed database delivery plan has another revision")
		}
		want["norn_"+strings.TrimPrefix(key, stagedPrefix)] = true
	}
	if len(items) != len(want) {
		return fmt.Errorf("managed database delivery has missing or extra items")
	}
	for key := range want {
		if items[key] == "" {
			return fmt.Errorf("managed database delivery lacks %s", key)
		}
	}
	for key := range items {
		if !want[key] {
			return fmt.Errorf("managed database delivery has an unexpected item")
		}
	}
	for _, name := range plan.RuntimeDatabaseNames {
		if expectedTargets[name] == "" || items[DatabaseTargetItemKey(name)] != expectedTargets[name] {
			return fmt.Errorf("managed database target differs for %s", name)
		}
	}
	return c.DeliverDatabaseVariable(region, plan.JobID, items, plan.DatabaseRevision)
}
