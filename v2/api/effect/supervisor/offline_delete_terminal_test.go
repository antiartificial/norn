package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/effect"
	"norn/v2/api/fleet/offline"
)

func offlineTerminalFixture(t *testing.T, manager *Manager, identity effect.ExecutionIdentity) ([]byte, []byte) {
	t.Helper()
	c := offline.DeleteCommitment{
		SchemaVersion: offline.DeleteCommitmentSchema, PlanID: uuid.NewString(), AttemptID: uuid.NewString(), GrantID: uuid.NewString(),
		BindingSHA256: strings.Repeat("a", 64), AttemptSHA256: strings.Repeat("b", 64), EffectGrantSHA256: strings.Repeat("c", 64),
		SourceCommit: strings.Repeat("d", 40), SavedPlanSHA256: strings.Repeat("e", 64), EffectSHA256: strings.Repeat("f", 64),
		ProviderAccountUUID: "work-account", TargetID: "work-staging", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID,
		Backend:         offline.Backend{Bucket: "test", Key: "state", VersionID: "version", Lineage: "lineage", Serial: 8, RawSHA256: strings.Repeat("1", 64)},
		FenceGeneration: 3, AuthorityEpoch: 4, RunnerPrincipalSubject: "runner", RunnerTokenID: "token", RunnerDeviceID: "device",
		SupervisorRootID: manager.RootID(), SupervisorExecutionID: identity.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID,
		RetryPolicy: offline.DeleteRetryPolicy, CommittedAt: time.Now().UTC(),
	}
	commitment, err := json.Marshal(c)
	if err != nil || c.Validate() != nil {
		t.Fatalf("test commitment: %v", err)
	}
	dispatch, err := json.Marshal(offlineDeleteDispatchBinding{
		SchemaVersion: "norn.fleet-offline-delete-dispatch/v1", PlanID: c.PlanID, AttemptID: c.AttemptID,
		GrantID: c.GrantID, CommitmentSHA256: offlineDeleteSHA(commitment), TargetID: c.TargetID,
		DropletID: c.DropletID, FenceGeneration: c.FenceGeneration, AuthorityEpoch: c.AuthorityEpoch,
		SupervisorRootID: c.SupervisorRootID, SupervisorExecutionID: c.SupervisorExecutionID,
		RuntimeInstanceID: c.RuntimeInstanceID, State: "admitted", RetryPolicy: c.RetryPolicy,
		AdmittedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return commitment, dispatch
}

func launchedOfflineTerminalRuntime(t *testing.T) (*Manager, *backendFake, effect.Reservation, effect.ExecutionIdentity) {
	t.Helper()
	backend := newBackendFake()
	manager := testManager(t, t.TempDir(), backend)
	material := testMaterial()
	payload, err := manager.BuildTestDescriptor(material)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{
		Authority: "authority", Resource: "fleet/work-staging/delete", OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage: "offline.delete", Supervisor: "norn-effect-runner", SupervisorExecutionID: uuid.NewString(), LaunchPayload: payload,
	}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.Launch(context.Background(), reservation, material)
	if err != nil {
		t.Fatal(err)
	}
	return manager, backend, reservation, identity
}

func terminalFakeChild(context.Context, effect.ExecutionIdentity) (offlineDeleteChildObservation, error) {
	return offlineDeleteChildObservation{
		ResponseStatus: 204, ExitCode: 0, ChildExited: true, ContainmentProven: true,
		CredentialRevoked: true, RestartDisabled: true,
		ChildEvidenceSHA256: strings.Repeat("2", 64), CredentialEvidenceSHA256: strings.Repeat("3", 64),
		ObservedAt: time.Now().Add(time.Second).UTC(),
	}, nil
}

func TestOfflineDeleteTerminalSurvivesRestartAndBindsExactSources(t *testing.T) {
	manager, backend, reservation, identity := launchedOfflineTerminalRuntime(t)
	commitment, dispatch := offlineTerminalFixture(t, manager, identity)
	if _, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, commitment,
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { return 204, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := manager.recordOfflineDeleteTerminal(context.Background(), reservation, identity, dispatch, commitment, terminalFakeChild)
	if err != nil || got.ResponseStatus != 204 || !got.CredentialRevoked {
		t.Fatalf("terminal=%+v err=%v", got, err)
	}
	restarted, err := NewManager(manager.root, testSigningKey, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.readOfflineDeleteTerminal(reservation, identity, dispatch, commitment); err != nil {
		t.Fatalf("restart read: %v", err)
	}
	if _, err := restarted.recordOfflineDeleteTerminal(context.Background(), reservation, identity, dispatch, commitment, terminalFakeChild); err == nil {
		t.Fatal("second marker write succeeded")
	}
	changed := append([]byte(nil), dispatch...)
	changed[len(changed)-2] ^= 1
	if _, err := restarted.readOfflineDeleteTerminal(reservation, identity, changed, commitment); err == nil {
		t.Fatal("changed dispatch was accepted")
	}
	changed = append([]byte(nil), commitment...)
	changed[len(changed)-2] ^= 1
	if _, err := restarted.readOfflineDeleteTerminal(reservation, identity, dispatch, changed); err == nil {
		t.Fatal("changed commitment was accepted")
	}
	changedIdentity := identity
	changedIdentity.RuntimeInstanceID = uuid.NewString()
	if _, err := restarted.readOfflineDeleteTerminal(reservation, changedIdentity, dispatch, commitment); err == nil {
		t.Fatal("changed runtime was accepted")
	}
}

func TestOfflineDeleteTerminalRefusesAmbiguousChildAndIncompleteCredential(t *testing.T) {
	for name, alter := range map[string]func(*offlineDeleteChildObservation){
		"lost response":      func(o *offlineDeleteChildObservation) { o.ResponseStatus = 0 },
		"wrong status":       func(o *offlineDeleteChildObservation) { o.ResponseStatus = 202 },
		"child running":      func(o *offlineDeleteChildObservation) { o.ChildExited = false },
		"descendant running": func(o *offlineDeleteChildObservation) { o.ContainmentProven = false },
		"credential live":    func(o *offlineDeleteChildObservation) { o.CredentialRevoked = false },
		"restart enabled":    func(o *offlineDeleteChildObservation) { o.RestartDisabled = false },
		"bad exit":           func(o *offlineDeleteChildObservation) { o.ExitCode = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			manager, _, reservation, identity := launchedOfflineTerminalRuntime(t)
			commitment, dispatch := offlineTerminalFixture(t, manager, identity)
			lost := errors.New("response lost")
			_, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, commitment,
				func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
				func(context.Context) (int, error) {
					if name == "lost response" {
						return 0, lost
					}
					return 204, nil
				})
			if name == "lost response" {
				if !errors.Is(err, lost) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			_, err = manager.recordOfflineDeleteTerminal(context.Background(), reservation, identity, dispatch, commitment,
				func(ctx context.Context, id effect.ExecutionIdentity) (offlineDeleteChildObservation, error) {
					observation, _ := terminalFakeChild(ctx, id)
					alter(&observation)
					return observation, nil
				})
			if err == nil {
				t.Fatal("ambiguous or incomplete child was marked terminal")
			}
			if _, err := manager.readOfflineDeleteTerminal(reservation, identity, dispatch, commitment); err == nil {
				t.Fatal("terminal marker existed after refused child")
			}
		})
	}
}

func TestOfflineDeleteTerminalRejectsTamperAndMissingHistory(t *testing.T) {
	manager, backend, reservation, identity := launchedOfflineTerminalRuntime(t)
	commitment, dispatch := offlineTerminalFixture(t, manager, identity)
	if _, err := manager.dispatchOfflineDeleteOnce(context.Background(), reservation, identity, commitment,
		func(context.Context, effect.ExecutionIdentity, []byte) error { return nil },
		func(context.Context) (int, error) { return 204, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.recordOfflineDeleteTerminal(context.Background(), reservation, identity, dispatch, commitment, terminalFakeChild); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(manager.root, testSigningKey, backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		path := filepath.Join(directory, offlineDeleteTerminalName)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data[len(data)-2] ^= 1
		return os.WriteFile(path, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.readOfflineDeleteTerminal(reservation, identity, dispatch, commitment); err == nil {
		t.Fatal("tampered terminal marker was accepted")
	}
	if err := restarted.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		return os.Remove(filepath.Join(directory, offlineDeleteDispatchIntentName))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.readOfflineDeleteTerminal(reservation, identity, dispatch, commitment); err == nil {
		t.Fatal("terminal marker survived missing dispatch intent")
	}
}
