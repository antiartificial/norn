package worker

import (
	"context"
	"fmt"

	"norn/v2/api/effect"
)

type ClaimedDeploymentEffectStore interface {
	DeploymentJobEffectStore
	UnresolvedForResource(context.Context, string, string) (effect.Record, bool, error)
}

// AdvanceClaimedFleetDeploymentJob submits at most once and reconciles the
// exact Nomad job revision to healthy allocations. A pending or uncertain
// observation keeps the effect gate and yields a deferred worker result.
func AdvanceClaimedFleetDeploymentJob(ctx context.Context, effects ClaimedDeploymentEffectStore, remote DeploymentJobRemote,
	plan ClaimedFleetDeploymentJobPlan) (effect.Token, error) {
	if effects == nil || remote == nil || plan.Job == nil || plan.Reservation.InputDigest == "" {
		return effect.Token{}, fmt.Errorf("claimed Fleet Nomad step is unavailable")
	}
	decision, err := EnsureDeploymentJobEffect(ctx, effects, remote, plan.Reservation, plan.Job)
	if err != nil {
		return effect.Token{}, err
	}
	if decision.Healthy {
		return decision.Token, nil
	}
	pending := func(reason string) (effect.Token, error) {
		return effect.Token{}, &effect.PendingError{EffectID: decision.EffectID, Resource: plan.Reservation.Resource, Reason: reason}
	}
	if decision.State != DeploymentJobEffectObserved || !decision.Attempted || decision.Token.EffectID == "" {
		return pending("Nomad submission has no verified job revision")
	}
	record, found, err := effects.UnresolvedForResource(ctx, plan.Reservation.Authority, plan.Reservation.Resource)
	if err != nil {
		return effect.Token{}, err
	}
	if !found || record.Token != decision.Token || record.Reservation.InputDigest != plan.Reservation.InputDigest ||
		record.Reservation.SupervisorExecutionID != plan.Reservation.SupervisorExecutionID || record.Lifecycle != effect.LifecycleLaunched {
		return pending("launched Nomad effect changed before health observation")
	}
	health, err := CompleteDeploymentJobEffect(ctx, effects, remote, record)
	if err != nil {
		return effect.Token{}, err
	}
	if !health.Healthy || health.Token != decision.Token {
		return pending("Nomad job health is not yet verified")
	}
	return health.Token, nil
}
