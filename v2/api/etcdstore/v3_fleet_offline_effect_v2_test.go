package etcdstore

import (
	"context"
	"encoding/json"
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
		ProviderExact: true, LatestBackendExact: true, AllNamespacesInspected: true,
		ProviderTargetPresent: true, BackendTargetPresent: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
}

func (f *offlineEffectV2Fixture) terminalObservation(grant offline.EffectGrantV2, at time.Time) offline.TerminalObservationV2 {
	return offline.TerminalObservationV2{PlanID: f.binding.PlanID, AttemptID: f.attempt.ID, GrantID: grant.ID,
		BindingSHA256: f.attempt.BindingSHA256, TargetID: f.binding.TargetID, TargetFenceGeneration: f.binding.FenceGeneration,
		AuthorityEpoch: 1, WriterFence: grant.WriterFence, ObservedAt: at,
		EvidenceSHA256: strings.Repeat("1", 64), ProviderEvidenceSHA256: strings.Repeat("2", 64),
		BackendEvidenceSHA256: strings.Repeat("3", 64), WorkloadEvidenceSHA256: strings.Repeat("4", 64),
		ProviderAbsent: true, BackendAdvanced: true, BackendTargetAbsent: true, AllNamespacesInspected: true,
		WorkloadTargetAbsent: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
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

func (f *offlineEffectV2Fixture) terminal(t *testing.T) offline.EffectGrantV2 {
	t.Helper()
	grant := f.issue(t)
	f.commit(t, grant)
	dispatchAt := f.now.Add(3 * time.Second)
	if _, err := f.h.s.DispatchOfflineDeleteV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.effectObservation(dispatchAt), dispatchAt); err != nil {
		t.Fatal(err)
	}
	terminalAt := f.now.Add(5 * time.Second)
	observation := f.terminalObservation(grant, terminalAt)
	if _, err := f.h.s.RecordOfflineTerminalV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, terminalAt); err != nil {
		t.Fatal(err)
	}
	return grant
}

func (f *offlineEffectV2Fixture) completionObservation(grant offline.EffectGrantV2, at time.Time) offline.CompletionObservationV2 {
	return offline.CompletionObservationV2{PlanID: f.binding.PlanID, AttemptID: f.attempt.ID, GrantID: grant.ID,
		BindingSHA256: f.attempt.BindingSHA256, TargetID: f.binding.TargetID,
		TargetFenceGeneration: f.binding.FenceGeneration, AuthorityEpoch: 1, WriterFence: grant.WriterFence,
		TerminalEvidenceSHA256: strings.Repeat("1", 64), ReceiptSHA256: strings.Repeat("5", 64),
		ProviderEvidenceSHA256: strings.Repeat("2", 64), BackendEvidenceSHA256: strings.Repeat("3", 64),
		WorkloadEvidenceSHA256: strings.Repeat("4", 64), ProviderAbsent: true, BackendAdvanced: true,
		BackendTargetAbsent: true, AllNamespacesInspected: true, WorkloadTargetAbsent: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true,
		ReleasedAt: at.Add(-time.Second), ObservedAt: at}
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

func TestOfflineEffectV2RequiresTargetPresentBeforeDeleteDispatch(t *testing.T) {
	for name, mutate := range map[string]func(*offline.EffectObservationV2){
		"provider absent": func(o *offline.EffectObservationV2) { o.ProviderTargetPresent = false },
		"backend absent":  func(o *offline.EffectObservationV2) { o.BackendTargetPresent = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOfflineEffectV2Fixture(t)
			at := f.now.Add(time.Second)
			observation := f.effectObservation(at)
			mutate(&observation)
			if _, err := f.h.s.IssueOfflineEffectGrantV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, f.proof, observation, at); !errors.Is(err, offline.ErrEffectProofV2) {
				t.Fatalf("pre-delete absence admitted: %v", err)
			}
		})
	}
}

func TestOfflineEffectV2DispatchRechecksTargetPresent(t *testing.T) {
	for name, mutate := range map[string]func(*offline.EffectObservationV2){
		"provider absent": func(o *offline.EffectObservationV2) { o.ProviderTargetPresent = false },
		"backend absent":  func(o *offline.EffectObservationV2) { o.BackendTargetPresent = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOfflineEffectV2Fixture(t)
			grant := f.issue(t)
			f.commit(t, grant)
			at := f.now.Add(3 * time.Second)
			observation := f.effectObservation(at)
			mutate(&observation)
			if _, err := f.h.s.DispatchOfflineDeleteV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, at); !errors.Is(err, offline.ErrEffectProofV2) {
				t.Fatalf("dispatch without live target admitted: %v", err)
			}
		})
	}
}

func TestOfflineEffectV2TerminalRejectsTargetStillPresent(t *testing.T) {
	for name, mutate := range map[string]func(*offline.TerminalObservationV2){
		"provider present": func(o *offline.TerminalObservationV2) { o.ProviderAbsent = false },
		"backend present":  func(o *offline.TerminalObservationV2) { o.BackendTargetAbsent = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOfflineEffectV2Fixture(t)
			grant := f.issue(t)
			f.commit(t, grant)
			at := f.now.Add(3 * time.Second)
			if _, err := f.h.s.DispatchOfflineDeleteV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.effectObservation(at), at); err != nil {
				t.Fatal(err)
			}
			terminalAt := f.now.Add(5 * time.Second)
			observation := f.terminalObservation(grant, terminalAt)
			mutate(&observation)
			if _, err := f.h.s.RecordOfflineTerminalV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, terminalAt); !errors.Is(err, offline.ErrTerminalProofV2) {
				t.Fatalf("terminal target presence admitted: %v", err)
			}
		})
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

func TestOfflineEffectV2CompletionIsAtomicAndIdempotent(t *testing.T) {
	f := newOfflineEffectV2Fixture(t)
	grant := f.terminal(t)
	completedAt := f.now.Add(6 * time.Second)
	results := make(chan struct {
		attempt offline.Attempt
		err     error
	}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			attempt, err := f.h.s.CompleteOfflineAttemptV2(context.Background(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.completionObservation(grant, completedAt), completedAt)
			results <- struct {
				attempt offline.Attempt
				err     error
			}{attempt, err}
		}()
	}
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil || result.attempt.Status != "succeeded" || result.attempt.CurrentPhase != "complete" || result.attempt.FinishedAt == nil || !result.attempt.FinishedAt.Equal(completedAt) {
			t.Fatalf("completed=%+v err=%v", result.attempt, result.err)
		}
	}
	fence, _, err := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
	if err != nil || fence.Held || fence.LastRelease == nil || fence.LastRelease.PlanID != f.binding.PlanID || fence.LastRelease.Reason != "succeeded" {
		t.Fatalf("fence=%+v err=%v", fence, err)
	}
	if _, err := f.h.s.AdvanceFleetAuthorityEpoch(f.h.ctx(), 1, "post-completion"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.auth.RevokeAccessToken(f.h.ctx(), f.proof.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.s.kv.Delete(f.h.ctx(), f.h.s.fleetTargetFenceKey(f.binding.TargetID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.s.kv.Delete(f.h.ctx(), f.h.s.offlineEffectV2Key(f.binding.PlanID)); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, offline.CompletionObservationV2{}, f.proof.TokenExpiresAt)
	if err != nil || replayed.FinishedAt == nil || !replayed.FinishedAt.Equal(completedAt) {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestOfflineEffectV2CompletionRequiresTerminalEvidenceAndLiveLease(t *testing.T) {
	t.Run("terminal evidence", func(t *testing.T) {
		f := newOfflineEffectV2Fixture(t)
		grant := f.issue(t)
		if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, offline.CompletionObservationV2{}, f.now.Add(2*time.Second)); !errors.Is(err, offline.ErrTerminalProofV2) {
			t.Fatalf("completion before terminal=%v", err)
		}
		fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
		if !fence.Held {
			t.Fatal("completion before terminal released fence")
		}
	})
	t.Run("runner lease", func(t *testing.T) {
		f := newOfflineEffectV2Fixture(t)
		grant := f.terminal(t)
		if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.completionObservation(grant, f.proof.TokenExpiresAt), f.proof.TokenExpiresAt); err == nil {
			t.Fatal("expired runner lease completed attempt")
		}
		fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
		if !fence.Held {
			t.Fatal("expired runner lease released fence")
		}
	})
	t.Run("fresh postgres release receipt", func(t *testing.T) {
		f := newOfflineEffectV2Fixture(t)
		grant := f.terminal(t)
		at := f.now.Add(6 * time.Second)
		observation := f.completionObservation(grant, at)
		observation.ObservedAt = at.Add(-31 * time.Second)
		observation.ReleasedAt = observation.ObservedAt
		if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, at); !errors.Is(err, offline.ErrCompletionProofV2) {
			t.Fatalf("stale release receipt=%v", err)
		}
		fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
		if !fence.Held {
			t.Fatal("stale release receipt released fence")
		}
	})
	t.Run("exact grant", func(t *testing.T) {
		f := newOfflineEffectV2Fixture(t)
		f.terminal(t)
		if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, uuid.NewString(), f.proof, offline.CompletionObservationV2{}, f.now.Add(6*time.Second)); !errors.Is(err, offline.ErrTerminalProofV2) {
			t.Fatalf("wrong grant completion=%v", err)
		}
		fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
		if !fence.Held {
			t.Fatal("wrong grant released fence")
		}
	})
	t.Run("authority epoch", func(t *testing.T) {
		f := newOfflineEffectV2Fixture(t)
		grant := f.terminal(t)
		if _, err := f.h.s.AdvanceFleetAuthorityEpoch(f.h.ctx(), 1, "completion-race"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.completionObservation(grant, f.now.Add(6*time.Second)), f.now.Add(6*time.Second)); err == nil {
			t.Fatal("superseded authority completed attempt")
		}
		fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
		if !fence.Held {
			t.Fatal("superseded authority released fence")
		}
	})
}

func TestOfflineEffectV2CompletionRejectsTargetStillPresent(t *testing.T) {
	for name, mutate := range map[string]func(*offline.CompletionObservationV2){
		"provider present": func(o *offline.CompletionObservationV2) { o.ProviderAbsent = false },
		"backend present":  func(o *offline.CompletionObservationV2) { o.BackendTargetAbsent = false },
	} {
		t.Run(name, func(t *testing.T) {
			f := newOfflineEffectV2Fixture(t)
			grant := f.terminal(t)
			at := f.now.Add(6 * time.Second)
			observation := f.completionObservation(grant, at)
			mutate(&observation)
			if _, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, observation, at); !errors.Is(err, offline.ErrCompletionProofV2) {
				t.Fatalf("completion target presence admitted: %v", err)
			}
			fence, _, _ := f.h.s.GetFleetTargetFence(f.h.ctx(), f.binding.TargetID)
			if !fence.Held {
				t.Fatal("failed completion released target fence")
			}
		})
	}
}

func TestOfflineAttemptRejectsCorruptCompletionEvidence(t *testing.T) {
	f := newOfflineEffectV2Fixture(t)
	grant := f.terminal(t)
	at := f.now.Add(6 * time.Second)
	attempt, err := f.h.s.CompleteOfflineAttemptV2(f.h.ctx(), f.binding.PlanID, f.attempt.ID, grant.ID, f.proof, f.completionObservation(grant, at), at)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*offline.Attempt){
		"receipt digest":    func(a *offline.Attempt) { a.Completion.ReceiptSHA256 = "" },
		"provider digest":   func(a *offline.Attempt) { a.Completion.ProviderEvidenceSHA256 = "" },
		"provider presence": func(a *offline.Attempt) { a.Completion.ProviderAbsent = false },
		"backend presence":  func(a *offline.Attempt) { a.Completion.BackendTargetAbsent = false },
		"writer holder":     func(a *offline.Attempt) { a.Completion.WriterFence.Holder = uuid.NewString() },
		"terminal digest":   func(a *offline.Attempt) { a.Completion.TerminalEvidenceSHA256 = "" },
		"timestamp order":   func(a *offline.Attempt) { a.Completion.RecordedAt = a.Completion.ObservedAt.Add(-time.Second) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copy := attempt
			completion := *attempt.Completion
			copy.Completion = &completion
			mutate(&copy)
			raw, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeOfflineAttempt(raw, copy.PlanID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
				t.Fatalf("corrupt completion decoded: %v", err)
			}
		})
	}
}
