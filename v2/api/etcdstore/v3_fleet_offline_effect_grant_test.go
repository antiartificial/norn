package etcdstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/offline"
	"norn/v2/api/store"
)

type offlineGrantFixture struct {
	h       *etcdExecutorHarness
	auth    *AuthStore
	binding offline.Binding
	attempt offline.Attempt
	proof   OfflineRunnerProof
	now     time.Time
}

func newOfflineGrantFixture(t *testing.T) *offlineGrantFixture {
	t.Helper()
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID, deviceID, tokenID := uuid.NewString(), uuid.NewString(), "norn_"+uuid.NewString()
	offlineTestPlan(h, planID)
	auth := NewAuthStore(h.s.kv, h.s.prefix)
	if err := auth.CreateAccessDevice(h.ctx(), &store.AccessDevice{ID: deviceID, Name: "offline",
		Platform: "fleet-offline-runner", CreatedAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	subject := "fleet-offline-device:" + deviceID
	if err := auth.RecordAccessToken(h.ctx(), &store.AccessToken{JTI: tokenID, DeviceID: deviceID, Subject: subject,
		Scopes: []string{"fleet:offline-attempt"}, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	proof, err := auth.ProveOfflineRunner(h.ctx(), subject, now)
	if err != nil {
		t.Fatal(err)
	}
	binding := offlineTestBinding(planID, targetID, now)
	binding.RunnerPrincipalSubject = subject
	binding, approval := offlineTestApproval(t, binding, now)
	if _, err := h.s.ReserveOfflineDispatchWithRunnerProof(h.ctx(), binding, approval, proof, now); err != nil {
		t.Fatal(err)
	}
	attempt, err := h.s.AdmitOfflineAttemptWithRunnerProof(h.ctx(), offlineAttemptClaim(binding), proof, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	return &offlineGrantFixture{h: h, auth: auth, binding: binding, attempt: attempt, proof: proof, now: now}
}

func TestOfflineEffectGrantIssueConsumeAndRecoverRealEtcd(t *testing.T) {
	f := newOfflineGrantFixture(t)
	grant, err := f.h.s.IssueOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(time.Second))
	if err != nil || grant.Phase != "issued" || grant.AuthorizesEffect || grant.RunnerTokenID != f.proof.TokenID {
		t.Fatalf("issue = %+v, %v", grant, err)
	}
	if _, err := f.h.s.IssueOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(2*time.Second)); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("duplicate issue = %v", err)
	}
	recovered, err := f.h.s.RecoverOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(2*time.Second))
	if err != nil || recovered != grant {
		t.Fatalf("issued recovery = %+v, %v", recovered, err)
	}
	consumed, err := f.h.s.ConsumeOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.now.Add(3*time.Second))
	if err != nil || consumed.Phase != "consumed" || consumed.ConsumedAt == nil || consumed.AuthorizesEffect {
		t.Fatalf("consume = %+v, %v", consumed, err)
	}
	if _, err := f.h.s.ConsumeOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.now.Add(4*time.Second)); !errors.Is(err, offline.ErrEffectGrantMismatch) {
		t.Fatalf("second consume = %v", err)
	}
	recovered, err = f.h.s.RecoverOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(4*time.Second))
	if err != nil || recovered.Phase != "consumed" || recovered.ConsumedAt == nil {
		t.Fatalf("consumed recovery = %+v, %v", recovered, err)
	}
}

func TestOfflineEffectGrantFailsClosedOnCredentialAndEventDriftRealEtcd(t *testing.T) {
	f := newOfflineGrantFixture(t)
	grant, err := f.h.s.IssueOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.RevokeAccessToken(context.Background(), f.proof.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.s.ConsumeOfflineEffectGrant(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.now.Add(2*time.Second)); !errors.Is(err, ErrOfflineEffectGrantConflict) {
		t.Fatalf("stale credential consume = %v", err)
	}

	other := newOfflineGrantFixture(t)
	if _, err := other.h.s.IssueOfflineEffectGrant(other.h.ctx(), other.binding.PlanID, other.attempt.ID, other.proof, other.now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := other.h.s.kv.Delete(other.h.ctx(), other.h.s.offlineEffectGrantEventKey(other.binding.PlanID, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := other.h.s.RecoverOfflineEffectGrant(other.h.ctx(), other.binding.PlanID, other.attempt.ID, other.proof, other.now.Add(2*time.Second)); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("partial history recovery = %v", err)
	}
}
