package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/effect"
	"norn/v2/api/model"
	"norn/v2/api/store"
)

func newScaleFoundationFixture(t *testing.T) (context.Context, *clientv3.Client, *V3OperationStore, store.OperationClaim, store.AppOperationLock, effect.Reservation) {
	t.Helper()
	endpoints := os.Getenv("NORN_TEST_ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	ctx := context.Background()
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoints, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-tests/scale-foundation/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("norn-etcd-scale-foundation-signing-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	authority := uuid.NewString()
	operations, err := NewV3OperationStore(client, prefix, authority, signer)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	op := model.Operation{ID: id, Kind: "app.scale", App: "widgets", Ref: "us-central/web", Status: model.OperationQueued, MaxAttempts: 2,
		Payload: map[string]interface{}{"app": "widgets", "group": "web", "region": "us-central", "nomadRegion": "global", "nomadJobId": "norn-job-pinned", "count": 3}}
	accepted := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: op.Kind, Resource: "app/widgets", Key: id},
		Operation: op, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"app": op.App, "group": "web", "region": "us-central", "count": 3}}
	accepted.Fingerprint, err = store.CanonicalOperationRequestFingerprint(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	claimed, claim, err := operations.ClaimNextOperation(ctx, "scale-worker", time.Minute, []string{"app.scale"})
	if err != nil || claimed == nil {
		t.Fatalf("claim scale operation: op=%+v err=%v", claimed, err)
	}
	lock, acquired, err := operations.AcquireAppOperationLock(ctx, op.App)
	if err != nil || !acquired {
		t.Fatalf("acquire scale app lock: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(lock.Release)
	payload, err := json.Marshal(scaleEffectInput{App: op.App, NomadJobID: "norn-job-pinned", Group: "web", Region: "us-central", NomadRegion: "global", Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	reservation := effect.Reservation{Authority: authority, Resource: "app/widgets/scale/us-central/web", Stage: "app.scale.nomad", Supervisor: "nomad-scale", SupervisorExecutionID: "nomad-scale-" + id,
		OperationClaim: effect.OperationClaim{OperationID: claim.OperationID(), OwnerID: claim.OwnerID(), Generation: claim.Generation()}, LaunchPayload: payload}
	reservation.InputDigest, err = effect.ComputeInputDigest(reservation)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, client, operations, claim, lock, reservation
}

func TestV3ScaleEffectReservationSurvivesWorkerCrashBeforeCompletion(t *testing.T) {
	ctx, _, operations, _, _, reservation := newScaleFoundationFixture(t)
	effects, err := NewV3ScaleEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	created, err := effects.Reserve(ctx, reservation)
	if err != nil || !created.Created {
		t.Fatalf("reserve scale effect: %+v err=%v", created, err)
	}

	// A new adapter instance models the process restart. It must recover the
	// exact immutable reservation instead of authorizing another Nomad call.
	restarted, err := NewV3ScaleEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := restarted.Reserve(ctx, reservation)
	if err != nil || replayed.Created || replayed.Record.Token != created.Record.Token {
		t.Fatalf("replay after restart: %+v err=%v", replayed, err)
	}
	changed := reservation
	changed.Authority = uuid.NewString()
	if _, err := restarted.Reserve(ctx, changed); err == nil {
		t.Fatal("foreign control authority reserved scale effect")
	}
}

func TestV3ScaleEffectWaitsForForeignNamespaceAppGate(t *testing.T) {
	ctx, _, operations, _, _, scaleReservation := newScaleFoundationFixture(t)
	canaryID := uuid.NewString()
	canaryOp := model.Operation{ID: canaryID, Kind: "app.canary-promote", App: "widgets", Status: model.OperationQueued, MaxAttempts: 1,
		Payload: map[string]interface{}{"app": "widgets", "region": "us-central", "nomadRegion": "global", "deploymentId": "deployment-" + canaryID}}
	accepted := store.OperationAcceptance{Identity: store.OperationRequestIdentity{Authority: scaleReservation.Authority, Actor: store.OperationActor{Issuer: "test", Subject: "operator"}, Kind: canaryOp.Kind, Resource: "app/widgets", Key: canaryID},
		Operation: canaryOp, Audit: store.AcceptanceAuditContext{Source: "test"}, Semantics: map[string]interface{}{"app": canaryOp.App, "deploymentId": canaryOp.Payload["deploymentId"]}}
	var err error
	accepted.Fingerprint, err = store.CanonicalOperationRequestFingerprint(accepted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Accept(ctx, accepted); err != nil {
		t.Fatal(err)
	}
	claimed, canaryClaim, err := operations.ClaimNextOperation(ctx, "canary-worker", time.Minute, []string{"app.canary-promote"})
	if err != nil || claimed == nil {
		t.Fatalf("claim canary operation: op=%+v err=%v", claimed, err)
	}
	canaryPayload, err := json.Marshal(canaryEffectInput{App: "widgets", Region: "us-central", NomadRegion: "global", DeploymentID: "deployment-" + canaryID})
	if err != nil {
		t.Fatal(err)
	}
	canaryReservation := effect.Reservation{Authority: scaleReservation.Authority, Resource: "app/widgets/canary-promote/us-central", Stage: "app.canary-promote.nomad", Supervisor: "nomad-canary-promotion", SupervisorExecutionID: "canary-" + canaryID,
		OperationClaim: effect.OperationClaim{OperationID: canaryClaim.OperationID(), OwnerID: canaryClaim.OwnerID(), Generation: canaryClaim.Generation()}, LaunchPayload: canaryPayload}
	canaryReservation.InputDigest, err = effect.ComputeInputDigest(canaryReservation)
	if err != nil {
		t.Fatal(err)
	}
	canaryEffects, err := NewV3CanaryEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	canary, err := canaryEffects.Reserve(ctx, canaryReservation)
	if err != nil || !canary.Created {
		t.Fatalf("reserve canary blocker: %+v err=%v", canary, err)
	}
	scaleEffects, err := NewV3ScaleEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	if record, found, err := scaleEffects.UnresolvedForResource(ctx, scaleReservation.Authority, scaleReservation.Resource); err != nil || found || record.Token.EffectID != "" {
		t.Fatalf("foreign namespace lookup should be wait-only: record=%+v found=%v err=%v", record, found, err)
	}
	if _, err := scaleEffects.Reserve(ctx, scaleReservation); !errors.Is(err, effect.ErrResourceBlocked) {
		t.Fatalf("scale reservation behind canary gate error=%v", err)
	}
	verification := effect.Verification{Decision: effect.VerificationNeverLaunched, InputDigest: canaryReservation.InputDigest, SupervisorExecutionID: canaryReservation.SupervisorExecutionID,
		EvidenceSource: "nomad.deployment", EvidenceReference: "deployment-" + canaryID, ObservedAt: time.Now().UTC()}
	if err := canaryEffects.Resolve(ctx, canary.Record.Token, effect.Resolution{Decision: effect.VerificationNeverLaunched, Verification: verification}); err != nil {
		t.Fatal(err)
	}
	if result, err := scaleEffects.Reserve(ctx, scaleReservation); err != nil || !result.Created {
		t.Fatalf("scale reservation after foreign gate resolved: %+v err=%v", result, err)
	}
}

func TestV3ScaleCompletionAtomicallyFencesClaimAppLockAndDesiredIntent(t *testing.T) {
	ctx, _, operations, claim, lock, reservation := newScaleFoundationFixture(t)
	effects, err := NewV3ScaleEffectReservations(operations)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := effects.Reserve(ctx, reservation)
	if err != nil {
		t.Fatal(err)
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: "nomad-eval:eval-1"}
	if err := effects.MarkLaunched(ctx, reserved.Record.Token, identity); err != nil {
		t.Fatal(err)
	}
	verification := effect.Verification{Decision: effect.VerificationSucceeded, InputDigest: reservation.InputDigest, SupervisorExecutionID: reservation.SupervisorExecutionID,
		RuntimeInstanceID: identity.RuntimeInstanceID, EvidenceSource: "nomad.scale-status", EvidenceReference: "widgets/web/eval-1", ResultDigest: effect.DigestInput([]byte(`{"desired":3}`)), ResultReference: "eval-1", ObservedAt: time.Now().UTC()}
	if err := effects.Complete(ctx, reserved.Record.Token, effect.Completion{Outcome: effect.OutcomeSucceeded, Verification: verification}); err != nil {
		t.Fatal(err)
	}

	foreign := store.NewFencedAppOperationLock(ctx, "foreign-fence", func() {})
	defer foreign.Release()
	err = operations.FinishScaleClaimedOperationWithAppLock(ctx, claim, foreign, reservation.Authority, "widgets", "web", "us-central", 3, "scaled", map[string]interface{}{"effectId": reserved.Record.Token.EffectID})
	if !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("foreign app lock completion error=%v", err)
	}
	counts, err := operations.DesiredReplicaCounts(ctx, "widgets", "us-central")
	if err != nil || len(counts) != 0 {
		t.Fatalf("failed completion wrote desired intent: counts=%v err=%v", counts, err)
	}
	if err := operations.FinishScaleClaimedOperationWithAppLock(ctx, claim, lock, uuid.NewString(), "widgets", "web", "us-central", 3, "scaled", nil); !errors.Is(err, store.ErrOperationOwnershipLost) {
		t.Fatalf("foreign authority completion error=%v", err)
	}
	negative := []struct {
		name            string
		process, region string
		count           int
		metadata        map[string]interface{}
	}{
		{name: "process", process: "worker", region: "us-central", count: 3, metadata: map[string]interface{}{"effectId": reserved.Record.Token.EffectID}},
		{name: "region", process: "web", region: "eu-west", count: 3, metadata: map[string]interface{}{"effectId": reserved.Record.Token.EffectID}},
		{name: "count", process: "web", region: "us-central", count: 4, metadata: map[string]interface{}{"effectId": reserved.Record.Token.EffectID}},
		{name: "effect", process: "web", region: "us-central", count: 3, metadata: map[string]interface{}{"effectId": uuid.NewString()}},
	}
	for _, test := range negative {
		if err := operations.FinishScaleClaimedOperationWithAppLock(ctx, claim, lock, reservation.Authority, "widgets", test.process, test.region, test.count, "scaled", test.metadata); err == nil {
			t.Fatalf("%s mismatch completed scale intent", test.name)
		}
	}
	if err := operations.FinishScaleClaimedOperationWithAppLock(ctx, claim, lock, reservation.Authority, "widgets", "web", "us-central", 3, "scaled", map[string]interface{}{"effectId": reserved.Record.Token.EffectID}); err != nil {
		t.Fatal(err)
	}
	counts, err = operations.DesiredReplicaCounts(ctx, "widgets", "us-central")
	if err != nil || counts["web"] != 3 {
		t.Fatalf("desired counts=%v err=%v", counts, err)
	}
	op, err := operations.GetOperation(ctx, claim.OperationID())
	if err != nil || op.Status != model.OperationSucceeded || op.Metadata["effectId"] != reserved.Record.Token.EffectID {
		t.Fatalf("terminal operation=%+v err=%v", op, err)
	}
}
