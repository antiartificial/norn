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
	"norn/v2/api/store"
)

func validPlatformReleaseIntent() PlatformReleaseIntent {
	return PlatformReleaseIntent{
		Name: "platform-oct21", Generation: 1, Environment: "staging",
		OldSourceSHA: strings.Repeat("a", 40), NewSourceSHA: strings.Repeat("b", 40),
		OldPublisherRunID: 123, PublisherRunID: 124,
		OldSignedManifestSHA256: strings.Repeat("c", 64), NewSignedManifestSHA256: strings.Repeat("d", 64),
		SignedReleaseTag: "platform-" + strings.Repeat("b", 40),
		ControlRoster: []PlatformReleaseControl{
			{Name: "control-01", TailnetIP: "100.1.2.3", PrivateIP: "10.1.2.3"},
			{Name: "control-02", TailnetIP: "100.1.2.4", PrivateIP: "10.1.2.4"},
			{Name: "control-03", TailnetIP: "100.1.2.5", PrivateIP: "10.1.2.5"},
		},
		StateSHA256: strings.Repeat("e", 64), HostKeysSHA256: strings.Repeat("f", 64),
	}
}

func TestPlatformReleaseIntentRejectsIncompleteBindings(t *testing.T) {
	valid := validPlatformReleaseIntent()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*PlatformReleaseIntent){
		"same source":       func(r *PlatformReleaseIntent) { r.NewSourceSHA = r.OldSourceSHA },
		"old publisher":     func(r *PlatformReleaseIntent) { r.OldPublisherRunID = 0 },
		"new manifest":      func(r *PlatformReleaseIntent) { r.NewSignedManifestSHA256 = "" },
		"roster":            func(r *PlatformReleaseIntent) { r.ControlRoster = nil },
		"duplicate control": func(r *PlatformReleaseIntent) { r.ControlRoster = append(r.ControlRoster, r.ControlRoster[0]) },
		"state digest":      func(r *PlatformReleaseIntent) { r.StateSHA256 = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := validPlatformReleaseIntent()
			mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatal("accepted incomplete binding")
			}
		})
	}
}

func TestPlatformReleaseIntentEtcdLifecycle(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoint == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/platform-release/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("platform-release-test-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := validPlatformReleaseIntent()
	record, created, err := s.EnsurePlatformReleaseIntent(ctx, request)
	if err != nil || !created {
		t.Fatalf("create: %v, %v", created, err)
	}
	if record.Status != "Pending" || record.IntentDigest == "" {
		t.Fatalf("unexpected record: %+v", record)
	}
	_, created, err = s.EnsurePlatformReleaseIntent(ctx, request)
	if err != nil || created {
		t.Fatalf("idempotent create: %v, %v", created, err)
	}
	changed := request
	changed.NewSignedManifestSHA256 = strings.Repeat("a", 64)
	if _, _, err = s.EnsurePlatformReleaseIntent(ctx, changed); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("expected conflicting request, got %v", err)
	}
	approved, err := s.ApprovePlatformReleaseIntent(ctx, request.Name, record.IntentDigest, "operator-test")
	if err != nil || approved.Status != "Approved" {
		t.Fatalf("approval: %+v %v", approved, err)
	}
	attempt, created, err := s.StartPlatformReleaseAttempt(ctx, request.Name, record.IntentDigest, "operation-12345678", "executor-12345678", "session-1234567890123456")
	if err != nil || !created {
		t.Fatalf("attempt: %+v %v", attempt, err)
	}
	if _, _, err = s.StartPlatformReleaseAttempt(ctx, request.Name, record.IntentDigest, "operation-other", "executor-other", "session-other-12345678"); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("expected active attempt conflict, got %v", err)
	}
	checkpoint := PlatformReleaseCheckpoint{Ordinal: 1, Phase: "preflight", Health: "healthy", EvidenceSHA256: strings.Repeat("e", 64)}
	updated, err := s.AppendPlatformReleaseCheckpoint(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, attempt.FenceEpoch, checkpoint)
	if err != nil || updated.NextOrdinal != 2 {
		t.Fatalf("checkpoint: %+v %v", updated, err)
	}
	if _, err = s.AppendPlatformReleaseCheckpoint(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, attempt.FenceEpoch, checkpoint); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("expected ordinal conflict, got %v", err)
	}
	if _, err = s.RenewPlatformReleaseAttempt(ctx, request.Name, attempt.ID, "wrong-owner", attempt.OwnerSession, attempt.FenceEpoch); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("expected owner fence, got %v", err)
	}
	if _, err = s.FinishPlatformReleaseAttempt(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, attempt.FenceEpoch, "Succeeded"); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListPlatformReleaseCheckpoints(ctx, request.Name)
	if err != nil || len(items) != 1 {
		t.Fatalf("checkpoints: %d %v", len(items), err)
	}
}

func TestPlatformReleaseRecoveryRequiresOperatorBindingAndFencesStaleOwner(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("NORN_TEST_ETCD_ENDPOINTS"))
	if endpoint == "" {
		t.Skip("NORN_TEST_ETCD_ENDPOINTS is not set")
	}
	client, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(endpoint, ","), DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	prefix := "/norn-test/platform-release-recovery/" + uuid.NewString()
	t.Cleanup(func() { _, _ = client.Delete(context.Background(), prefix, clientv3.WithPrefix()) })
	signer, err := store.NewHMACAcceptanceSigner("platform-release-recovery-key-000000")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewV3OperationStore(client, prefix, uuid.NewString(), signer)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := validPlatformReleaseIntent()
	intent, _, err := s.EnsurePlatformReleaseIntent(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApprovePlatformReleaseIntent(ctx, request.Name, intent.IntentDigest, "operator-test"); err != nil {
		t.Fatal(err)
	}
	attempt, _, err := s.StartPlatformReleaseAttempt(ctx, request.Name, intent.IntentDigest, "operation-12345678", "executor-12345678", "session-old-123456789")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinishPlatformReleaseAttempt(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, 1, "Failed"); err != nil {
		t.Fatal(err)
	}
	stopDigest, stateDigest := strings.Repeat("1", 64), strings.Repeat("2", 64)
	auth := PlatformReleaseRecoveryAuthorization{AttemptID: attempt.ID, ExpectedFenceEpoch: 1, IntentDigest: intent.IntentDigest, RecoveryNonce: strings.Repeat("n", 32), OldOwnerStopAttestationSHA256: stopDigest, ObservedSignedMemberStateSHA256: stateDigest, ApprovedBy: "operator-test"}
	if _, err = s.AuthorizePlatformReleaseRecovery(ctx, request.Name, auth); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("authorized before failed lease expiry: %v", err)
	}
	attempt.Status = "Failed"
	// Force an expired lease in the isolated test prefix; no automatic takeover
	// occurs even though the old process has missed its deadline.
	attempt.LeaseExpiresAt = time.Now().Add(-time.Minute)
	encoded, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Put(ctx, s.platformReleaseAttemptKey(request.Name), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecoverPlatformReleaseAttempt(ctx, request.Name, attempt.ID, "executor-12345678", "session-new-123456789", auth.RecoveryNonce, stopDigest, stateDigest, 1); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("recovered without authorization: %v", err)
	}
	if _, err = s.AuthorizePlatformReleaseRecovery(ctx, request.Name, auth); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.RecoverPlatformReleaseAttempt(ctx, request.Name, attempt.ID, "executor-12345678", "session-new-123456789", auth.RecoveryNonce, stopDigest, stateDigest, 1)
	if err != nil || recovered.FenceEpoch != 2 {
		t.Fatalf("recovery: %+v %v", recovered, err)
	}
	if _, err = s.RenewPlatformReleaseAttempt(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, 1); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("stale owner renew: %v", err)
	}
	checkpoint := PlatformReleaseCheckpoint{Ordinal: 1, Phase: "resume:verified", Health: "healthy", EvidenceSHA256: stateDigest}
	if _, err = s.AppendPlatformReleaseCheckpoint(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, 1, checkpoint); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("stale owner checkpoint: %v", err)
	}
	if _, err = s.FinishPlatformReleaseAttempt(ctx, request.Name, attempt.ID, attempt.OwnerID, attempt.OwnerSession, 1, "Succeeded"); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("stale owner finish: %v", err)
	}
	if _, err = s.FinishPlatformReleaseAttempt(ctx, request.Name, attempt.ID, recovered.OwnerID, recovered.OwnerSession, 2, "Succeeded"); err != nil {
		t.Fatal(err)
	}
	auth.ExpectedFenceEpoch = 2
	auth.RecoveryNonce = strings.Repeat("m", 32)
	if _, err = s.AuthorizePlatformReleaseRecovery(ctx, request.Name, auth); !errors.Is(err, ErrPlatformReleaseConflict) {
		t.Fatalf("restarted successful attempt: %v", err)
	}
}
