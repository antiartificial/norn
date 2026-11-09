package etcdstore

import (
	"testing"

	"norn/v2/api/fleet"
)

func TestFleetTerminalPlanRequiresCompleteProtectedPrestatePair(t *testing.T) {
	prestate := &fleet.ProtectedApplyPrestate{}
	rule := &fleet.ProtectedApplyTransitionRule{}
	for name, plan := range map[string]fleet.CapacityPlan{
		"legacy":          {},
		"complete pair":   {ProtectedApplyPrestate: prestate, ProtectedApplyTransitionRule: rule},
		"prestate only":   {ProtectedApplyPrestate: prestate},
		"transition only": {ProtectedApplyTransitionRule: rule},
	} {
		t.Run(name, func(t *testing.T) {
			hasPrestate, err := fleetTerminalPlanHasProtectedPrestate(plan)
			if name == "prestate only" || name == "transition only" {
				if err == nil {
					t.Fatal("partial protected plan was accepted as legacy")
				}
				return
			}
			if err != nil || hasPrestate != (name == "complete pair") {
				t.Fatalf("hasPrestate=%v err=%v", hasPrestate, err)
			}
		})
	}
}
