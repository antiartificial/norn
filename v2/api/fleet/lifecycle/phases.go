// Package lifecycle holds the Fleet controller's pure, zero-behavior-change
// helpers shared by the PG legacy, PG V3 and etcd V3 attempt backends (see
// docs/v3/fleet-controller/plan.md, §2.1). It imports only fleet, model and
// stdlib, and it never decides anything backend-specific: callers keep their
// own reads, commits, request schemas and the D1-D12 profile branches
// documented in docs/v3/fleet-controller/lifecycle-contract.md.
package lifecycle

import "fmt"

// Profile distinguishes the two phase shapes a plan's attempts can advance
// through. PostgresLegacy is the live, unsigned legacy attempt path;
// everything else (PG V3 and etcd V3) uses the canonical nine-phase list
// regardless of drain.
type Profile int

const (
	PostgresLegacy Profile = iota
	V3
)

// canonicalPhases is the nine-phase list shared by every profile that
// requires drain, and by every V3 profile regardless of drain (D4).
var canonicalPhases = []string{
	"prechange_verified",
	"provider_applying",
	"infrastructure_applied",
	"inventory_generated",
	"nodes_configured",
	"nodes_enrolled",
	"readiness_verified",
	"old_nodes_drained",
	"complete",
}

// legacyNoDrainPhases is the PG legacy six-phase list used only when the
// plan does not require drain (D3, D4): it starts at infrastructure_applied
// and has no old_nodes_drained phase.
var legacyNoDrainPhases = []string{
	"infrastructure_applied",
	"inventory_generated",
	"nodes_configured",
	"nodes_enrolled",
	"readiness_verified",
	"complete",
}

// Phases returns the ordered phase list for profile. requiresDrain only
// narrows PostgresLegacy: every V3 profile always uses the canonical
// nine-phase list (D4).
func Phases(profile Profile, requiresDrain bool) []string {
	if profile == PostgresLegacy && !requiresDrain {
		return legacyNoDrainPhases
	}
	return canonicalPhases
}

// NextPhase returns the phase after current for profile, or current with
// terminal=true when current is already the last phase. It returns an error
// when current is not a member of the profile's phase list.
func NextPhase(profile Profile, current string, requiresDrain bool) (next string, terminal bool, err error) {
	phases := Phases(profile, requiresDrain)
	for index, phase := range phases {
		if phase != current {
			continue
		}
		if index == len(phases)-1 {
			return current, true, nil
		}
		return phases[index+1], false, nil
	}
	return "", false, fmt.Errorf("current phase is not supported")
}

// ValidPhase reports whether phase is one of the nine canonical phases.
func ValidPhase(phase string) bool {
	for _, allowed := range canonicalPhases {
		if phase == allowed {
			return true
		}
	}
	return false
}

// RequiresDrain reports whether a capacity-plan action requires drain.
// Callers keep their own number decoding for currentDesired and
// proposedDesired (m5): the three existing copies decode differently
// (int/int64/float64 vs float64/json.Number), and that difference is
// preserved by keeping decoding backend-local.
func RequiresDrain(action string, currentDesired, proposedDesired float64) bool {
	if action == "replace" {
		return true
	}
	return action == "scale" && proposedDesired < currentDesired
}
