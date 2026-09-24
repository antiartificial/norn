package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"norn/v2/api/database"
	"norn/v2/api/effect"
	"norn/v2/api/effect/supervisor"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

type SnapshotEffects struct {
	Executor                 *effect.Executor
	Store                    *store.PGEffectStore
	Manager                  *supervisor.Manager
	PGDumpPath, PGDumpSHA256 string
	Timeout                  time.Duration
}

type SnapshotExecutionUnavailableError struct{}

func (*SnapshotExecutionUnavailableError) Error() string {
	return "durable app.snapshot execution is unavailable"
}

func (s *SnapshotEffects) available() bool {
	return s != nil && s.Executor != nil && s.Store != nil && s.Manager != nil && s.PGDumpPath != "" && len(s.PGDumpSHA256) == 64 && s.Timeout > 0
}

type snapshotEffectSupervisor struct{ m *supervisor.Manager }

func (s snapshotEffectSupervisor) Prepare(c context.Context, r effect.Reservation) error {
	return s.m.Prepare(c, r)
}
func (s snapshotEffectSupervisor) Launch(context.Context, effect.Reservation, effect.LaunchMaterial) (effect.ExecutionIdentity, error) {
	return effect.ExecutionIdentity{}, fmt.Errorf("snapshot requires private launch material")
}
func (s snapshotEffectSupervisor) LaunchPrivate(c context.Context, r effect.Reservation, v any) (effect.ExecutionIdentity, error) {
	m, ok := v.(supervisor.SnapshotLaunchMaterial)
	if !ok {
		return effect.ExecutionIdentity{}, fmt.Errorf("invalid snapshot private material")
	}
	return s.m.LaunchSnapshot(c, r, m)
}
func (s snapshotEffectSupervisor) Query(c context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.m.ObserveSnapshot(c, r, id)
}
func (s snapshotEffectSupervisor) Revoke(c context.Context, r effect.Reservation, id effect.ExecutionIdentity) (effect.Observation, error) {
	return s.m.Revoke(c, r, id)
}
func (s snapshotEffectSupervisor) RetrieveResult(c context.Context, r effect.Reservation, id effect.ExecutionIdentity, ref string) ([]byte, error) {
	return s.m.RetrieveSnapshotResult(c, r, id, ref)
}
func NewSnapshotEffects(db *store.DB, m *supervisor.Manager, path, digest string, timeout time.Duration) (*SnapshotEffects, error) {
	st, e := store.NewPGEffectStore(db)
	if e != nil {
		return nil, e
	}
	v, e := supervisor.NewVerifier(m)
	if e != nil {
		return nil, e
	}
	s := &SnapshotEffects{Store: st, Manager: m, PGDumpPath: path, PGDumpSHA256: digest, Timeout: timeout}
	s.Executor = &effect.Executor{Store: st, Supervisor: snapshotEffectSupervisor{m}, Verifier: v}
	if !s.available() {
		return nil, fmt.Errorf("snapshot effects incomplete")
	}
	return s, nil
}
func snapshotResource(app string) string { return "app/" + app + "/snapshot" }
func snapshotExecutionID(r effect.Reservation) string {
	x := sha256.Sum256([]byte(r.Authority + "\x00" + r.OperationClaim.OperationID + "\x00" + r.InputDigest + "\x00" + fmt.Sprint(r.OperationClaim.Generation)))
	return "snapshot-" + hex.EncodeToString(x[:16])
}

// ReconcilePublishedArtifacts removes private dumps only for snapshot effects
// whose owning operation has a durable succeeded record. It closes the crash
// window after the terminal operation CAS but before the worker callback.
func (s *SnapshotEffects) ReconcilePublishedArtifacts(ctx context.Context) error {
	if !s.available() {
		return fmt.Errorf("snapshot effects incomplete")
	}
	records, err := s.Store.CompletedSnapshotOperations(ctx, s.Manager.RootID())
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := s.Manager.DiscardDurablyTerminalSnapshotArtifact(ctx, record); err != nil {
			return err
		}
	}
	failed, abandoned, err := s.Store.TerminalSnapshotCleanupEffects(ctx, s.Manager.RootID())
	if err != nil {
		return err
	}
	for _, record := range failed {
		if err := s.Manager.DiscardDurablyTerminalSnapshotArtifact(ctx, record); err != nil {
			return err
		}
	}
	for _, record := range abandoned {
		if err := s.Manager.DiscardAbandonedSnapshotArtifact(ctx, record.Reservation, record.Execution); err != nil {
			return err
		}
	}
	return nil
}

func (s *SnapshotEffects) DiscardPublishedArtifact(ctx context.Context, operationID string) error {
	record, found, err := s.Store.LatestForOperation(ctx, operationID, "app.snapshot")
	if err != nil || !found {
		return fmt.Errorf("durable snapshot effect record unavailable: %w", err)
	}
	return s.Manager.DiscardSnapshotArtifact(ctx, record.Reservation, record.Execution)
}

func (s *SnapshotEffects) DiscardFailedArtifact(ctx context.Context, operationID string) error {
	record, found, err := s.Store.LatestForOperation(ctx, operationID, "app.snapshot")
	if err != nil || !found {
		return fmt.Errorf("durable snapshot effect record unavailable: %w", err)
	}
	if record.Lifecycle != effect.LifecycleCompleted || record.Completion == nil || record.Completion.Outcome != effect.OutcomeFailed {
		return fmt.Errorf("durable snapshot effect is not a verified terminal failure")
	}
	return s.Manager.DiscardFailedSnapshotArtifact(ctx, record.Reservation, record.Execution)
}
func (p *Pipeline) executeAttestedSnapshot(ctx context.Context, op *model.Operation, claim store.OperationClaim, bound *boundDatabase, loc snapshotLocation) (*dataSnapshot, effect.ExecuteResult, error) {
	if !p.SnapshotEffects.available() || p.DB == nil || bound == nil || bound.session == nil {
		return nil, effect.ExecuteResult{}, &SnapshotExecutionUnavailableError{}
	}
	if e := p.DB.CheckOperationClaim(ctx, claim); e != nil {
		return nil, effect.ExecuteResult{}, &effect.PendingError{Resource: snapshotResource(op.App), Reason: "snapshot claim is no longer current", Cause: e}
	}
	authority, e := p.SnapshotEffects.Store.Authority(ctx)
	if e != nil {
		return nil, effect.ExecuteResult{}, e
	}
	var result effect.ExecuteResult
	var reservation effect.Reservation
	e = bound.session.WithSnapshotLaunchMaterial(database.SnapshotLaunchOptions{PGDumpPath: p.SnapshotEffects.PGDumpPath, PGDumpSHA256: p.SnapshotEffects.PGDumpSHA256, Subject: "app:" + op.App + "/db:" + bound.resolved.Target.BindingID + "@generation:" + fmt.Sprint(bound.resolved.Target.BindingGeneration), Timeout: p.SnapshotEffects.Timeout}, func(material supervisor.SnapshotLaunchMaterial) error {
		payload, e := p.SnapshotEffects.Manager.BuildSnapshotDescriptor(material)
		if e != nil {
			return e
		}
		reservation = effect.Reservation{Authority: authority, Resource: snapshotResource(op.App), OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, Stage: "app.snapshot", Supervisor: "snapshot-runner", LaunchPayload: payload}
		if reservation.InputDigest, e = effect.ComputeInputDigest(reservation); e != nil {
			return e
		}
		reservation.SupervisorExecutionID = snapshotExecutionID(reservation)
		result, e = p.SnapshotEffects.Executor.Execute(ctx, effect.ExecuteRequest{Reservation: reservation, PrivateLaunch: material})
		return e
	})
	if e != nil {
		return nil, result, e
	}
	if result.Outcome != effect.OutcomeSucceeded {
		if result.Outcome == effect.OutcomeFailed {
			if cleanupErr := p.SnapshotEffects.DiscardFailedArtifact(ctx, op.ID); cleanupErr != nil {
				return nil, result, fmt.Errorf("release failed snapshot artifact admission: %w", cleanupErr)
			}
		}
		return nil, result, fmt.Errorf("snapshot effect failed")
	}
	rec, found, e := p.SnapshotEffects.Store.LatestForOperation(ctx, op.ID, "app.snapshot")
	if e != nil || !found {
		return nil, result, fmt.Errorf("durable snapshot effect record unavailable: %w", e)
	}
	var manifest supervisor.SnapshotManifest
	if e = json.Unmarshal(result.Output, &manifest); e != nil {
		return nil, result, e
	}
	created, e := PublishAttestedSnapshot(loc, op.StartedAt.UTC(), AttestedSnapshotArtifact{OperationID: op.ID, ClaimGeneration: claim.Generation(), SHA256: manifest.Artifact.SHA256, Size: manifest.Artifact.Bytes, Fence: func(publish func() error) error { return p.DB.WithOperationClaimFence(ctx, claim, publish) }, Copy: func(w io.Writer) error {
		_, e := p.SnapshotEffects.Manager.CopySnapshotArtifact(ctx, rec.Reservation, rec.Execution, w)
		return e
	}})
	return created, result, e
}
