package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"norn/v2/api/effect"
)

type migrationCheckerFake struct {
	result MigrationPostconditionResult
	checks int
}

func (f *migrationCheckerFake) CheckMigrationPostcondition(_ context.Context, _ MigrationIntent) (MigrationPostconditionResult, error) {
	f.checks++
	return f.result, nil
}

func TestMigrationVerifierRequiresContainedSuccessAndOriginalTarget(t *testing.T) {
	backend := newBackendFake()
	root := t.TempDir()
	manager := testManager(t, root, backend)
	command := "true"
	digest := sha256.Sum256([]byte(command))
	intent := testMigrationIntent()
	intent.CommandSHA256 = hex.EncodeToString(digest[:])
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/migrate",
		OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage:          MigrationStage, Supervisor: "migration-runner", SupervisorExecutionID: "migration-verifier", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity, err := manager.LaunchMigration(context.Background(), reservation, MigrationLaunchMaterial{Command: command, Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	record := effect.Record{Reservation: reservation, Execution: identity}
	checker := &migrationCheckerFake{result: MigrationPostconditionResult{TargetSHA256: intent.TargetSHA256,
		PostconditionSHA256: intent.PostconditionSHA256, Satisfied: true}}
	verifier, err := NewMigrationVerifier(manager, checker)
	if err != nil {
		t.Fatal(err)
	}
	exitCode := 0
	backend.states[reservation.SupervisorExecutionID] = BackendState{Phase: effect.SupervisorSucceeded,
		ExitCode: &exitCode, ContainmentProven: true, EvidenceReference: "migration/" + identity.RuntimeInstanceID}
	observation, err := manager.ObserveMigration(context.Background(), reservation, identity)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := verifier.Verify(context.Background(), record, observation)
	if err != nil || verification.Decision != effect.VerificationSucceeded || checker.checks != 1 {
		t.Fatalf("verified migration=%+v checks=%d err=%v", verification, checker.checks, err)
	}
	reservedBeforeAck := record
	reservedBeforeAck.Execution = effect.ExecutionIdentity{}
	if recovered, err := verifier.Verify(context.Background(), reservedBeforeAck, observation); err != nil || recovered.RuntimeInstanceID != identity.RuntimeInstanceID {
		t.Fatalf("migration committed before launch acknowledgement could not recover: %+v err=%v", recovered, err)
	}
	// Reopening the supervisor after the API process is lost must preserve
	// the original runtime identity and never hand the command to the backend
	// a second time, even when the durable effect lacks launch acknowledgement.
	restarted := testManager(t, root, backend)
	resumed, err := restarted.LaunchMigration(context.Background(), reservation, MigrationLaunchMaterial{Command: command, Directory: t.TempDir()})
	if err != nil || resumed.RuntimeInstanceID != identity.RuntimeInstanceID || backend.starts != 1 {
		t.Fatalf("restarted migration launch = %+v, starts=%d, err=%v", resumed, backend.starts, err)
	}
	restartedObservation, err := restarted.ObserveMigration(context.Background(), reservation, resumed)
	if err != nil {
		t.Fatal(err)
	}
	restartedVerifier, err := NewMigrationVerifier(restarted, checker)
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := restartedVerifier.Verify(context.Background(), reservedBeforeAck, restartedObservation); err != nil || recovered.RuntimeInstanceID != identity.RuntimeInstanceID {
		t.Fatalf("restarted verification = %+v, err=%v", recovered, err)
	}
	for _, mutate := range []struct {
		name  string
		apply func(*MigrationPostconditionResult)
	}{
		{"postcondition false", func(r *MigrationPostconditionResult) { r.Satisfied = false }},
		{"different target", func(r *MigrationPostconditionResult) { r.TargetSHA256 = "different" }},
		{"different check", func(r *MigrationPostconditionResult) { r.PostconditionSHA256 = "different" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			result := checker.result
			mutate.apply(&result)
			checker.result = result
			if _, err := verifier.Verify(context.Background(), record, observation); err == nil {
				t.Fatal("unproven database postcondition approved migration")
			}
			checker.result = MigrationPostconditionResult{TargetSHA256: intent.TargetSHA256, PostconditionSHA256: intent.PostconditionSHA256, Satisfied: true}
		})
	}
	checks := checker.checks
	for _, state := range []BackendState{
		{Phase: effect.SupervisorSucceeded, ExitCode: &exitCode, ContainmentProven: false, EvidenceReference: "uncontained"},
		{Phase: effect.SupervisorFailed, ExitCode: intPointer(1), ContainmentProven: true, EvidenceReference: "partial-write"},
		{Phase: effect.SupervisorSucceeded, ExitCode: &exitCode, ContainmentProven: true, TimedOut: true, EvidenceReference: "timeout"},
	} {
		backend.states[reservation.SupervisorExecutionID] = state
		candidate, err := manager.ObserveMigration(context.Background(), reservation, identity)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := verifier.Verify(context.Background(), record, candidate); err == nil {
			t.Fatalf("unsafe command state approved: %+v", state)
		}
	}
	if checker.checks != checks {
		t.Fatalf("unsafe command states reached database checker: before=%d after=%d", checks, checker.checks)
	}
	if _, err := NewMigrationVerifier(manager, nil); err == nil {
		t.Fatal("migration verifier accepted no database checker")
	}
}

func intPointer(value int) *int { return &value }

func TestMigrationNeverLaunchedRevocationIsAuthenticated(t *testing.T) {
	manager := testManager(t, t.TempDir(), newBackendFake())
	intent := testMigrationIntent()
	commandHash := sha256.Sum256([]byte("true"))
	intent.CommandSHA256 = hex.EncodeToString(commandHash[:])
	payload, err := manager.BuildMigrationDescriptor(intent)
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: "authority", Resource: "app/demo/migrate",
		OperationClaim: effect.OperationClaim{OperationID: "operation", OwnerID: "worker", Generation: 1},
		Stage:          MigrationStage, Supervisor: "migration-runner", SupervisorExecutionID: "migration-never-launched", LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Prepare(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID}
	checker := &migrationCheckerFake{}
	verifier, err := NewMigrationVerifier(manager, checker)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := manager.ObserveMigration(context.Background(), reservation, identity)
	if err != nil || observation.Phase != effect.SupervisorNotFound {
		t.Fatalf("registered migration=%+v err=%v", observation, err)
	}
	verified, err := verifier.Verify(context.Background(), effect.Record{Reservation: reservation}, observation)
	if err != nil || verified.Decision != effect.VerificationNeverLaunched {
		t.Fatalf("registered migration proof=%+v err=%v", verified, err)
	}
	revoked, err := manager.RevokeMigration(context.Background(), reservation, identity)
	if err != nil || revoked.Phase != effect.SupervisorNotFound {
		t.Fatalf("revoked migration=%+v err=%v", revoked, err)
	}
	verified, err = verifier.Verify(context.Background(), effect.Record{Reservation: reservation}, revoked)
	if err != nil || verified.Decision != effect.VerificationNeverLaunched || checker.checks != 0 {
		t.Fatalf("revocation proof=%+v checks=%d err=%v", verified, checker.checks, err)
	}
	if _, err := manager.LaunchMigration(context.Background(), reservation, MigrationLaunchMaterial{Command: "true", Directory: t.TempDir()}); err == nil {
		t.Fatal("revoked migration was relaunched")
	}
}
