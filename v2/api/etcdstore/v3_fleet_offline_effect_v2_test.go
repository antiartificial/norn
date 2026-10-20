package etcdstore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/fleet/offline"
	"norn/v2/api/store"
)

type offlineEffectV2Fixture struct {
	h       *etcdExecutorHarness
	auth    *AuthStore
	binding offline.Binding
	attempt offline.Attempt
	proof   OfflineRunnerProof
	now     time.Time
}

func offlineProviderNodesV2() []offline.ProviderNode {
	names := []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"}
	nodes := make([]offline.ProviderNode, len(names))
	for i, name := range names {
		id := "70000000" + string(rune('1'+i))
		if name == offline.WorkNode {
			id = offline.WorkDropletID
		}
		nodes[i] = offline.ProviderNode{Name: name, ProviderID: id}
	}
	return nodes
}

func newOfflineEffectV2Fixture(t *testing.T) *offlineEffectV2Fixture {
	t.Helper()
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID, deviceID, tokenID := uuid.NewString(), uuid.NewString(), "norn_"+uuid.NewString()
	offlineTestPlan(h, planID)
	auth := NewAuthStore(h.s.kv, h.s.prefix)
	if err := auth.CreateAccessDevice(h.ctx(), &store.AccessDevice{ID: deviceID, Name: "offline-v2", Platform: "fleet-offline-runner", CreatedAt: now.Add(-time.Hour)}); err != nil {
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
	binding.SchemaVersion = offline.BindingSchemaV2
	binding.CandidateSHA256 = strings.Repeat("9", 64)
	binding.ProviderNodes = offlineProviderNodesV2()
	binding.RunnerPrincipalSubject = subject
	binding, approval := offlineTestApproval(t, binding, now)
	if _, err := h.s.ReserveOfflineDispatchWithRunnerProof(h.ctx(), binding, approval, proof, now); err != nil {
		t.Fatal(err)
	}
	attempt, err := h.s.AdmitOfflineAttemptWithRunnerProof(h.ctx(), offlineAttemptClaim(binding), proof, uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	return &offlineEffectV2Fixture{h: h, auth: auth, binding: binding, attempt: attempt, proof: proof, now: now}
}

func (f *offlineEffectV2Fixture) effectObservation(at time.Time) offline.EffectObservationV2 {
	return offline.EffectObservationV2{PlanID: f.binding.PlanID, AttemptID: f.attempt.ID, BindingSHA256: f.attempt.BindingSHA256,
		TargetID: f.binding.TargetID, FenceGeneration: f.binding.FenceGeneration, AuthorityEpoch: 1,
		SourceCommit: f.binding.SourceCommit, SavedPlanSHA256: f.binding.SavedPlanSHA256, EffectSHA256: f.binding.EffectSHA256,
		Backend: f.binding.Backend, ProviderAccountUUID: f.binding.ProviderAccountUUID, NodeName: f.binding.NodeName, DropletID: f.binding.DropletID,
		WriterFence: offline.WriterFence{BackendID: "work-staging-postgres", Generation: 41, Holder: f.attempt.ID, SessionSHA256: strings.Repeat("8", 64)},
		ObservedAt:  at.Add(-time.Second), ValidUntil: at.Add(20 * time.Second), EvidenceSHA256: strings.Repeat("7", 64),
		ProviderExact: true, LatestBackendExact: true, AllNamespacesInspected: true, TargetEmpty: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
}

func (f *offlineEffectV2Fixture) issue(t *testing.T) offline.EffectGrantV2 {
	t.Helper()
	at := f.now.Add(time.Second)
	grant, err := f.h.s.IssueOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.effectObservation(at), at)
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func (f *offlineEffectV2Fixture) commit(t *testing.T, grant offline.EffectGrantV2) offline.EffectGrantV2 {
	t.Helper()
	result, err := f.h.s.CommitOfflineDeleteV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof,
		strings.Repeat("6", 64), offline.ExactDeleteRequestSHA256V2(), f.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestOfflineEffectV2LifecycleOneDispatchWinnerAndTerminal(t *testing.T) {
	f := newOfflineEffectV2Fixture(t)
	grant := f.issue(t)
	if grant.TargetFenceGeneration != f.binding.FenceGeneration || grant.WriterFence.Generation == grant.TargetFenceGeneration {
		t.Fatalf("target and writer generations not distinct: %+v", grant)
	}
	f.commit(t, grant)
	at := f.now.Add(3 * time.Second)
	observation := f.effectObservation(at)
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, err := f.h.s.DispatchOfflineDeleteV2(context.Background(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, at)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrOfflineEffectV2Conflict) && !errors.Is(err, offline.ErrDeleteCommitmentV2) {
			t.Fatalf("dispatch loser=%v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("dispatch winners=%d", winners)
	}
	recovered, err := f.h.s.RecoverOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, f.now.Add(4*time.Second))
	if err != nil || recovered.Phase != "delete_dispatched" || recovered.Dispatch == nil {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	terminalAt := f.now.Add(5 * time.Second)
	terminal := offline.TerminalObservationV2{PlanID: f.binding.PlanID, AttemptID: f.attempt.ID, GrantID: grant.ID,
		BindingSHA256: f.attempt.BindingSHA256, TargetID: f.binding.TargetID, TargetFenceGeneration: f.binding.FenceGeneration,
		AuthorityEpoch: 1, WriterFence: grant.WriterFence, ObservedAt: terminalAt,
		EvidenceSHA256: strings.Repeat("1", 64), ProviderEvidenceSHA256: strings.Repeat("2", 64),
		BackendEvidenceSHA256: strings.Repeat("3", 64), WorkloadEvidenceSHA256: strings.Repeat("4", 64),
		ProviderAbsent: true, BackendAdvanced: true, BackendTargetAbsent: true, AllNamespacesInspected: true,
		WorkloadTargetAbsent: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
	if _, err := f.h.s.RecordOfflineTerminalV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, terminal, terminalAt); err != nil {
		t.Fatal(err)
	}
	final, err := f.h.s.GetOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID)
	if err != nil || final.Phase != "terminal_verified" || final.Terminal == nil {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}

func TestOfflineEffectV2RejectsStaleEvidence(t *testing.T) {
	f := newOfflineEffectV2Fixture(t)
	at := f.now.Add(time.Minute)
	observation := f.effectObservation(at)
	observation.ObservedAt = at.Add(-31 * time.Second)
	if _, err := f.h.s.IssueOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, observation, at); !errors.Is(err, offline.ErrEffectProofV2) {
		t.Fatalf("stale evidence=%v", err)
	}
}

func TestOfflineEffectV2RejectsAuthorityDrift(t *testing.T) {
	tests := map[string]func(*offlineEffectV2Fixture){
		"owner": func(f *offlineEffectV2Fixture) {
			_, _ = f.h.s.kv.Put(f.h.ctx(), f.h.s.offlineOwnerConsumptionKey(f.binding.OwnerAuthorizationID), "changed")
		},
		"target fence": func(f *offlineEffectV2Fixture) {
			_, _ = f.h.s.kv.Delete(f.h.ctx(), f.h.s.fleetTargetFenceKey(f.binding.TargetID))
		},
		"epoch":  func(f *offlineEffectV2Fixture) { _, _ = f.h.s.AdvanceFleetAuthorityEpoch(f.h.ctx(), 1, "test-drift") },
		"device": func(f *offlineEffectV2Fixture) { _, _ = f.auth.RevokeAccessDevice(f.h.ctx(), f.proof.DeviceID) },
		"token":  func(f *offlineEffectV2Fixture) { _, _ = f.auth.RevokeAccessToken(f.h.ctx(), f.proof.TokenID) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newOfflineEffectV2Fixture(t)
			at := f.now.Add(time.Second)
			observation := f.effectObservation(at)
			mutate(f)
			if _, err := f.h.s.IssueOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, observation, at); err == nil {
				t.Fatal("drift admitted")
			}
		})
	}
}
