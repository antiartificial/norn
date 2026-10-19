package worker

import (
	"fmt"
	"sort"

	"norn/v2/api/model"
)

// validateClaimedDesiredReplicaCounts rejects a scale ledger that no longer
// fits the newly accepted InfraSpec. The caller holds the app lock while it
// reads the ledger and prepares the job, so a newer scale cannot race this
// deployment. An operator must reconcile the stored scale intent explicitly
// before submitting a different revision.
func validateClaimedDesiredReplicaCounts(spec *model.InfraSpec, region string, counts map[string]int) error {
	if spec == nil {
		return fmt.Errorf("Fleet desired replica reconciliation requires a deployable InfraSpec")
	}
	groups := make([]string, 0, len(counts))
	for group := range counts {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		count := counts[group]
		process, found := spec.Processes[group]
		if !found || process.Schedule != "" || process.Function != nil || process.Health == nil || !spec.ProcessRunsInRegion(process, region) {
			return fmt.Errorf("Fleet desired replica intent for group %q is ineligible in region %q; reconcile the stored scale intent before deploying", group, region)
		}
		minimum, maximum := 1, process.DeclaredReplicaCount()
		if process.Scaling != nil {
			if process.Scaling.Min > 0 {
				minimum = process.Scaling.Min
			}
			if process.Scaling.Max > 0 {
				maximum = process.Scaling.Max
			}
		}
		if maximum < minimum || count < minimum || count > maximum || process.HostPort > 0 && count > 1 {
			return fmt.Errorf("Fleet desired replica intent for group %q has count %d outside the newly accepted InfraSpec bounds; reconcile the stored scale intent before deploying", group, count)
		}
	}
	return nil
}
