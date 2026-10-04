package lifecycle

import (
	"fmt"
	"sort"

	"norn/v2/api/fleet"
)

// ValidateLineage checks that attempts form a single unbroken retry chain
// rooted at attempt 1, with RootAttemptID and RetryOf consistent at every
// step. It is order-insensitive: it reads attempts in ascending Attempt
// order from an internal copy and never mutates the slice it is given, so
// callers that depend on their own slice's order (ascending or descending)
// after the call keep that order unchanged.
func ValidateLineage(attempts []fleet.RunnerAttempt) error {
	if len(attempts) == 0 {
		return nil
	}
	ordered := append([]fleet.RunnerAttempt(nil), attempts...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Attempt < ordered[j].Attempt })
	root := ordered[0]
	if root.Attempt != 1 || root.RootAttemptID != root.ID || root.RetryOf != "" {
		return fmt.Errorf("fleet runner-attempt root lineage is corrupt")
	}
	previous := root.ID
	for index := 1; index < len(ordered); index++ {
		item := ordered[index]
		if item.Attempt != index+1 || item.RootAttemptID != root.ID || item.RetryOf != previous {
			return fmt.Errorf("fleet runner-attempt retry lineage is corrupt")
		}
		previous = item.ID
	}
	return nil
}
