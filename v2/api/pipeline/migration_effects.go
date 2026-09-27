package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

// MigrationEffects binds the common effect executor to the private migration
// runner and original-target verifier. It is not enabled merely by creation;
// deployment and standalone paths must supply the accepted reservation,
// pinned source assertion and private session material.
type MigrationEffects struct {
	Store   *store.PGEffectStore
	Manager *supervisor.Manager
	Timeout time.Duration
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

func NewMigrationEffects(db *store.DB, manager *supervisor.Manager, timeout time.Duration) (*MigrationEffects, error) {
	if db == nil || manager == nil || timeout <= 0 || timeout > supervisor.MaxMigrationTimeout {
		return nil, fmt.Errorf("migration effects require a store, supervisor and bounded timeout")
	}
	stored, err := store.NewPGEffectStore(db)
	if err != nil {
		return nil, err
	}
	return &MigrationEffects{Store: stored, Manager: manager, Timeout: timeout}, nil
}

func migrationEffectResource(bindingID string) string { return "database/" + bindingID + "/migration" }

func migrationExecutionID(reservation effect.Reservation) string {
	digest := sha256.Sum256([]byte(reservation.Authority + "\x00" + reservation.OperationClaim.OperationID + "\x00" + reservation.InputDigest))
	return "migration-" + hex.EncodeToString(digest[:16])
}

func (p *Pipeline) runSupervisedMigration(ctx context.Context, st *state, bound *boundDatabase) error {
	config := p.MigrationEffects
	if config == nil || config.Store == nil || config.Manager == nil || config.Timeout <= 0 ||
		p.DB == nil || p.DatabaseTargets == nil || p.DatabaseTargets.Secrets == nil ||
		bound == nil || bound.session == nil || st == nil || st.spec == nil {
		return fmt.Errorf("supervised migration is unavailable")
	}
	if st.claim.OperationID() == "" || st.claim.OwnerID() == "" || st.claim.Generation() <= 0 ||
		!strings.HasPrefix(st.sourceIdentity, "sha256:") || len(st.sourceIdentity) != len("sha256:")+64 {
		return fmt.Errorf("supervised migration requires a claim and pinned source checkpoint")
	}
	if bound.resolved.Target.Engine != database.EnginePostgreSQL {
		return fmt.Errorf("private migration launch for this database engine is not qualified")
	}
	if st.workDir == "" {
		return fmt.Errorf("supervised migration requires a prepared source checkout")
	}
	prepared, err := model.LoadInfraSpec(filepath.Join(st.workDir, "infraspec.yaml"))
	if err != nil || prepared.Migrations != st.spec.Migrations ||
		prepared.MigrationPostcondition == nil ||
		*prepared.MigrationPostcondition != *st.spec.MigrationPostcondition ||
		prepared.EffectiveMigrationDatabase() != st.spec.EffectiveMigrationDatabase() {
		return fmt.Errorf("prepared source migration differs from the accepted InfraSpec")
	}
	if err := p.DB.CheckOperationClaim(ctx, st.claim); err != nil {
		return &effect.PendingError{Resource: migrationEffectResource(bound.resolved.Target.BindingID), Reason: "migration claim is no longer current", Cause: err}
	}
	check, checkDigest, err := migrationPostconditionForSpec(st.spec, bound.resolved.Target)
	if err != nil {
		return err
	}
	intent, err := migrationIntentForAcceptedTarget(bound.resolved.Target, strings.TrimPrefix(st.sourceIdentity, "sha256:"),
		st.spec.Migrations, checkDigest, config.Timeout)
	if err != nil {
		return err
	}
	payload, err := config.Manager.BuildMigrationDescriptor(intent)
	if err != nil {
		return err
	}
	authority, err := config.Store.Authority(ctx)
	if err != nil {
		return &effect.PendingError{Resource: migrationEffectResource(bound.resolved.Target.BindingID), Reason: "control authority is unavailable", Cause: err}
	}
	reservation := effect.Reservation{Authority: authority, Resource: migrationEffectResource(bound.resolved.Target.BindingID),
		OperationClaim: effect.OperationClaim{OperationID: st.claim.OperationID(), OwnerID: st.claim.OwnerID(), Generation: st.claim.Generation()},
		Stage:          supervisor.MigrationStage, Supervisor: "migration-runner", LaunchPayload: payload}
	if reservation.InputDigest, err = effect.ComputeInputDigest(reservation); err != nil {
		return err
	}
	reservation.SupervisorExecutionID = migrationExecutionID(reservation)
	checker := &database.SQLMigrationPostconditionChecker{Resolved: bound.resolved, Secrets: p.DatabaseTargets.Secrets, Spec: check}
	executor, err := config.executorFor(checker)
	if err != nil {
		return err
	}
	valueEnv, fileEnv := migrationEnvNames(st.spec)
	return bound.session.WithMigrationLaunchMaterial(st.spec.Migrations, st.workDir, valueEnv, fileEnv, func(material supervisor.MigrationLaunchMaterial) error {
		request := effect.ExecuteRequest{Reservation: reservation, PrivateLaunch: material}
		result, executeErr := executor.Execute(ctx, request)
		if errors.Is(executeErr, effect.ErrResourceBlocked) {
			blocking, found, lookupErr := config.Store.UnresolvedForResource(ctx, authority, reservation.Resource)
			if lookupErr != nil || !found {
				return &effect.PendingError{Resource: reservation.Resource, Reason: "blocking migration lookup failed", Cause: lookupErr}
			}
			if _, recoverErr := executor.Recover(ctx, blocking); recoverErr != nil {
				return recoverErr
			}
			result, executeErr = executor.Execute(ctx, request)
		}
		if executeErr != nil {
			return executeErr
		}
		if result.Outcome != effect.OutcomeSucceeded {
			return fmt.Errorf("migration effect did not verify success")
		}
		return nil
	})
}

// executorFor binds one accepted target and pinned source assertion. A
// successor with changed source or target cannot approve an older migration;
// it remains pending until the original inputs are recovered or reviewed.
func (m *MigrationEffects) executorFor(checker supervisor.MigrationPostconditionChecker) (*effect.Executor, error) {
	if m == nil || m.Store == nil || m.Manager == nil {
		return nil, fmt.Errorf("migration effects are unavailable")
	}
	verifier, err := supervisor.NewMigrationVerifier(m.Manager, checker)
	if err != nil {
		return nil, err
	}
	return &effect.Executor{Store: m.Store, Supervisor: migrationEffectSupervisor{m.Manager}, Verifier: verifier}, nil
}

var _ effect.Supervisor = migrationEffectSupervisor{}
var _ effect.PrivateLaunchSupervisor = migrationEffectSupervisor{}
