package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineDeleteStoreFixture struct {
	grant                                       offline.EffectGrant
	attempt                                     offline.Attempt
	commitment                                  *offline.DeleteCommitment
	terminal                                    *offline.TerminalEvidence
	commitWrites, terminalWrites, observerCalls int
}

func (f *offlineDeleteStoreFixture) GetOfflineAttempt(context.Context, string) (offline.Attempt, error) {
	return f.attempt, nil
}

func (f *offlineDeleteStoreFixture) GetOfflineDeleteCommitment(context.Context, string) (offline.DeleteCommitment, []byte, error) {
	if f.commitment == nil {
		return offline.DeleteCommitment{}, nil, etcdstore.ErrNotFound
	}
	return *f.commitment, []byte(`{"committed":true}`), nil
}
func (f *offlineDeleteStoreFixture) GetOfflineEffectGrant(context.Context, string) (offline.EffectGrant, error) {
	return f.grant, nil
}
func (f *offlineDeleteStoreFixture) CommitOfflineDelete(_ context.Context, plan, attempt, grant string, identity etcdstore.OfflineDeleteRuntimeIdentity) (offline.DeleteCommitment, []byte, error) {
	f.commitWrites++
	value := offline.DeleteCommitment{PlanID: plan, AttemptID: attempt, GrantID: grant, RunnerPrincipalSubject: f.attempt.PrincipalSubject, RunnerTokenID: f.attempt.PrincipalTokenID, RunnerDeviceID: f.attempt.PrincipalDeviceID, SupervisorRootID: identity.SupervisorRootID, SupervisorExecutionID: identity.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID}
	f.commitment = &value
	return value, []byte(`{"committed":true}`), nil
}
func (f *offlineDeleteStoreFixture) GetOfflineTerminal(context.Context, string) (offline.TerminalEvidence, error) {
	if f.terminal == nil {
		return offline.TerminalEvidence{}, etcdstore.ErrNotFound
	}
	return *f.terminal, nil
}
func (f *offlineDeleteStoreFixture) RecordOfflineTerminal(ctx context.Context, plan, attempt, grant string, now time.Time, observer etcdstore.OfflineTerminalObserver) (offline.TerminalEvidence, error) {
	f.terminalWrites++
	f.observerCalls++
	if _, err := observer.ObserveOfflineTerminal(ctx, offline.Binding{}, offline.Attempt{}, f.grant); err != nil {
		return offline.TerminalEvidence{}, err
	}
	value := offline.TerminalEvidence{PlanID: plan, AttemptID: attempt, GrantID: grant, RecordedAt: now}
	f.terminal = &value
	return value, nil
}

type offlineRuntimeFixture struct{ calls int }

func (f *offlineRuntimeFixture) ResolveOfflineDeleteRuntime(context.Context, string, string, string) (etcdstore.OfflineDeleteRuntimeIdentity, error) {
	f.calls++
	return etcdstore.OfflineDeleteRuntimeIdentity{SupervisorRootID: "root", SupervisorExecutionID: "execution", RuntimeInstanceID: "runtime"}, nil
}

type offlineTerminalObserverFixture struct{ calls int }

func (f *offlineTerminalObserverFixture) ObserveOfflineTerminal(context.Context, offline.Binding, offline.Attempt, offline.EffectGrant) (offline.TerminalObservation, error) {
	f.calls++
	return offline.TerminalObservation{}, nil
}

func TestOfflineDeleteAuthorityRecoversLostResponsesWithoutRepeatingWork(t *testing.T) {
	const plan, attempt, grant = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	durable := &offlineDeleteStoreFixture{grant: offline.EffectGrant{PlanID: plan, AttemptID: attempt, ID: grant, Phase: "consumed"}}
	durable.attempt = offline.Attempt{ID: attempt, PrincipalSubject: "runner", PrincipalTokenID: "runner-token", PrincipalDeviceID: "device"}
	runtime := &offlineRuntimeFixture{}
	observer := &offlineTerminalObserverFixture{}
	authority := &serverOwnedOfflineDeleteAuthority{store: durable, runtime: runtime, observer: observer, clock: func() time.Time { return time.Now().UTC() }}
	for i := 0; i < 2; i++ {
		if value, _, err := authority.Commit(context.Background(), plan, attempt, etcdstore.OfflineRunnerIdentity{Subject: "runner", TokenID: "runner-token", DeviceID: "device"}); err != nil || value.AttemptID != attempt {
			t.Fatalf("commit recovery: %v", err)
		}
	}
	if durable.commitWrites != 1 || runtime.calls != 1 {
		t.Fatalf("commit replay repeated work: writes=%d resolver=%d", durable.commitWrites, runtime.calls)
	}
	if _, _, err := authority.Commit(context.Background(), plan, attempt, etcdstore.OfflineRunnerIdentity{Subject: "runner", TokenID: "replacement-token", DeviceID: "device"}); err == nil {
		t.Fatal("commitment recovery accepted a different runner token")
	}
	for i := 0; i < 2; i++ {
		if value, err := authority.RecordTerminal(context.Background(), plan, attempt); err != nil || value.AttemptID != attempt {
			t.Fatalf("terminal recovery: %v", err)
		}
	}
	if durable.terminalWrites != 1 || durable.observerCalls != 1 || observer.calls != 1 {
		t.Fatalf("terminal replay repeated observation: writes=%d observations=%d/%d", durable.terminalWrites, durable.observerCalls, observer.calls)
	}
}

func TestOfflineDeleteAuthorityRejectsRecoveryFromAnotherAttempt(t *testing.T) {
	const plan, attempt, otherAttempt, grant = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "44444444-4444-4444-8444-444444444444", "33333333-3333-4333-8333-333333333333"
	durable := &offlineDeleteStoreFixture{
		grant:      offline.EffectGrant{PlanID: plan, AttemptID: attempt, ID: grant, Phase: "consumed"},
		attempt:    offline.Attempt{ID: attempt, PrincipalSubject: "runner", PrincipalTokenID: "runner-token", PrincipalDeviceID: "device"},
		commitment: &offline.DeleteCommitment{PlanID: plan, AttemptID: otherAttempt, GrantID: grant, RunnerPrincipalSubject: "runner", RunnerTokenID: "runner-token", RunnerDeviceID: "device"},
		terminal:   &offline.TerminalEvidence{PlanID: plan, AttemptID: otherAttempt, GrantID: grant},
	}
	authority := &serverOwnedOfflineDeleteAuthority{
		store: durable, runtime: &offlineRuntimeFixture{}, observer: &offlineTerminalObserverFixture{},
		clock: func() time.Time { return time.Now().UTC() },
	}
	if _, _, err := authority.Commit(context.Background(), plan, attempt, etcdstore.OfflineRunnerIdentity{Subject: "runner", TokenID: "runner-token", DeviceID: "device"}); err == nil {
		t.Fatal("commitment from another attempt was accepted as recovery")
	}
	if _, err := authority.RecordTerminal(context.Background(), plan, attempt); err == nil {
		t.Fatal("terminal evidence from another attempt was accepted as recovery")
	}
	if durable.commitWrites != 0 || durable.terminalWrites != 0 {
		t.Fatalf("attempt mismatch mutated durable state: commit=%d terminal=%d", durable.commitWrites, durable.terminalWrites)
	}
}

type offlineDeleteRouteAuthority struct{ commits, terminals int }

func (a *offlineDeleteRouteAuthority) Commit(_ context.Context, plan, attempt string, _ etcdstore.OfflineRunnerIdentity) (offline.DeleteCommitment, []byte, error) {
	a.commits++
	return offline.DeleteCommitment{PlanID: plan, AttemptID: attempt}, nil, nil
}
func (a *offlineDeleteRouteAuthority) RecordTerminal(_ context.Context, plan, attempt string) (offline.TerminalEvidence, error) {
	a.terminals++
	return offline.TerminalEvidence{PlanID: plan, AttemptID: attempt}, nil
}

func TestOfflineDeleteMutationRoutesAcceptOnlyEmptyRequestsFromExactPrincipals(t *testing.T) {
	const plan, attempt = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	identities := offlineTestIdentity{device: &store.AccessDevice{ID: "device"}}
	trust := &offlineFleetTrust{runnerSubject: "runner", verifierSubject: "verifier"}
	runner := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "runner", TokenID: "runner-token", DeviceID: "device", Scopes: []string{handler.ScopeFleetOperate}}
	verifier := handler.AccessPrincipal{Source: handler.AccessPrincipalSourceManagedToken, Subject: "verifier", TokenID: "verifier-token", DeviceID: "device", Scopes: []string{handler.ScopeFleetVerify}}
	var active *handler.AccessPrincipal
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if active != nil {
				r = handler.WithAccessPrincipal(r, active)
			}
			next.ServeHTTP(w, r)
		})
	}
	authority := &offlineDeleteRouteAuthority{}
	router := chi.NewRouter()
	registerEtcdFleetOfflineDeleteMutationRoutes(router, auth, auth, identities, trust, authority)
	call := func(path, body string, principal *handler.AccessPrincipal) *httptest.ResponseRecorder {
		active = principal
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}
	commitPath := "/api/v1/fleet/offline/attempts/" + plan + "/" + attempt + "/delete-commitment"
	terminalPath := "/api/v1/fleet/offline/attempts/" + plan + "/" + attempt + "/terminal-evidence"
	if rec := call(commitPath, `{}`, &runner); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"authorizesEffect":false`) {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(terminalPath, `{}`, &verifier); rec.Code != http.StatusOK {
		t.Fatalf("terminal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(terminalPath, `{"proof":true}`, &verifier); rec.Code != http.StatusBadRequest {
		t.Fatalf("caller proof accepted: %d", rec.Code)
	}
	if rec := call(terminalPath, `{}`, &runner); rec.Code != http.StatusForbidden {
		t.Fatalf("runner recorded terminal: %d", rec.Code)
	}
	if rec := call(commitPath, `{}`, &verifier); rec.Code != http.StatusForbidden {
		t.Fatalf("verifier committed: %d", rec.Code)
	}
	if authority.commits != 1 || authority.terminals != 1 {
		t.Fatalf("unexpected mutations: %d %d", authority.commits, authority.terminals)
	}
}
