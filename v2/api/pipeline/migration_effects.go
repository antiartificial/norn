package pipeline

import (
	"context"
	"fmt"

	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/store"
)

// MigrationEffects binds the common effect executor to the private migration
// runner and original-target verifier. It is not enabled merely by creation;
// deployment and standalone paths must supply the accepted reservation,
// pinned source assertion and private session material.
type MigrationEffects struct {
	Executor *effect.Executor
	Store    *store.PGEffectStore
	Manager  *supervisor.Manager
}

type migrationEffectSupervisor struct{ manager *supervisor.Manager }

func (s migrationEffectSupervisor) Prepare(ctx context.Context, reservation effect.Reservation) error {
	return s.manager.Prepare(ctx, reservation)
}
func (migrationEffectSupervisor) Launch(context.Context, effect.Reservation, effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	return effect.ExecutionIdentity{}, fmt.Errorf("migration requires private launch material")
}
func (s migrationEffectSupervisor) LaunchPrivate(ctx context.Context, reservation effect.Reservation, value any) (effect.ExecutionIdentity, error) {
	material, ok := value.(supervisor.MigrationLaunchMaterial)
	if !ok {
		return effect.ExecutionIdentity{}, fmt.Errorf("migration private launch material is invalid")
	}
	return s.manager.LaunchMigration(ctx, reservation, material)
}
func (s migrationEffectSupervisor) Query(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.manager.ObserveMigration(ctx, reservation, identity)
}
func (s migrationEffectSupervisor) Revoke(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) (effect.Observation, error) {
	return s.manager.RevokeMigration(ctx, reservation, identity)
}
func (s migrationEffectSupervisor) RetrieveResult(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity, reference string) ([]byte, error) {
	return s.manager.RetrieveMigrationResult(ctx, reservation, identity, reference)
}

func NewMigrationEffects(db *store.DB, manager *supervisor.Manager, checker supervisor.MigrationPostconditionChecker) (*MigrationEffects, error) {
	if db == nil || manager == nil || checker == nil {
		return nil, fmt.Errorf("migration effects require a store, supervisor and original-target checker")
	}
	stored, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	verifier, err := supervisor.NewMigrationVerifier(manager, checker)
	if err != nil {
		return nil, err
	}
	return &MigrationEffects{Store: stored, Manager: manager,
		Executor: &effect.Executor{Store: stored, Supervisor: migrationEffectSupervisor{manager}, Verifier: verifier}}, nil
}

var _ effect.Supervisor = migrationEffectSupervisor{}
var _ effect.PrivateLaunchSupervisor = migrationEffectSupervisor{}
