package lifecycle

import (
	"norn/v2/api/fleet"
	"norn/v2/api/model"
)

// FromLegacy projects a PG legacy model.FleetRunnerAttempt onto the shared
// fleet.RunnerAttempt shape, so the pure lineage and expiry helpers in this
// package (and the fleet/fleettest HTTP-boundary conformance suite) can treat
// either backend's attempt uniformly. Fields with no home in fleet.RunnerAttempt
// (PilotRunID, PrincipalSubject, Metadata, Timing, ...) are PG-legacy-only and
// are intentionally dropped; this is a read-only projection and never writes
// back to a.
func FromLegacy(a *model.FleetRunnerAttempt) fleet.RunnerAttempt {
	if a == nil {
		return fleet.RunnerAttempt{}
	}
	return fleet.RunnerAttempt{
		SchemaVersion:           a.SchemaVersion,
		ID:                      a.ID,
		PlanID:                  a.PlanID,
		Attempt:                 a.Attempt,
		RunnerAttemptID:         a.RunnerAttemptID,
		Status:                  string(a.Status),
		CurrentPhase:            a.CurrentPhase,
		CommitSHA:               a.CommitSHA,
		PlanSHA256:              a.PlanSHA256,
		WorkflowURL:             a.WorkflowURL,
		RootAttemptID:           a.RootAttemptID,
		RetryOf:                 a.RetryOf,
		HeartbeatSequence:       a.HeartbeatSequence,
		HeartbeatTimeoutSeconds: a.HeartbeatTimeoutSeconds,
		Revision:                a.Revision,
		StartedAt:               a.StartedAt,
		HeartbeatAt:             a.HeartbeatAt,
		HeartbeatExpiresAt:      a.HeartbeatExpiresAt,
		UpdatedAt:               a.UpdatedAt,
		FinishedAt:              a.FinishedAt,
		LastError:               a.LastError,
	}
}
