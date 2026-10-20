package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
)

type offlineEffectV2HTTPStore struct {
	grant         offline.EffectGrantV2
	dispatchCalls int
}

func (s *offlineEffectV2HTTPStore) IssueOfflineEffectGrantV2(context.Context, string, string, etcdstore.OfflineRunnerProof, offline.EffectObservationV2, time.Time) (offline.EffectGrantV2, error) {
	return s.grant, nil
}
func (s *offlineEffectV2HTTPStore) CommitOfflineDeleteV2(context.Context, string, string, string, etcdstore.OfflineRunnerProof, string, string, time.Time) (offline.EffectGrantV2, error) {
	return s.grant, nil
}
func (s *offlineEffectV2HTTPStore) DispatchOfflineDeleteV2(_ context.Context, plan, attempt, grant string, _ etcdstore.OfflineRunnerProof, _ offline.EffectObservationV2, now time.Time) (offline.DeleteDispatchV2, error) {
	s.dispatchCalls++
	d := offline.DeleteDispatchV2{SchemaVersion: offline.DeleteDispatchSchemaV2, PlanID: plan, AttemptID: attempt, GrantID: grant, AdmittedAt: now}
	s.grant.Dispatch = &d
	return d, nil
}
func (s *offlineEffectV2HTTPStore) RecordOfflineTerminalV2(context.Context, string, string, string, etcdstore.OfflineRunnerProof, offline.TerminalObservationV2, time.Time) (offline.TerminalEvidenceV2, error) {
	return offline.TerminalEvidenceV2{}, nil
}
func (s *offlineEffectV2HTTPStore) RecoverOfflineEffectGrantV2(context.Context, string, string, etcdstore.OfflineRunnerProof, time.Time) (offline.EffectGrantV2, error) {
	return s.grant, nil
}

type offlineEffectV2HTTPSource struct{ effectCalls int }

func (s *offlineEffectV2HTTPSource) ObserveOfflineEffect(context.Context, string, string) (offline.EffectObservationV2, error) {
	s.effectCalls++
	return offline.EffectObservationV2{}, nil
}
func (s *offlineEffectV2HTTPSource) ObserveOfflineTerminal(context.Context, string, string, string) (offline.TerminalObservationV2, error) {
	return offline.TerminalObservationV2{}, nil
}

func offlineEffectV2Router(store offlineEffectV2Store, source OfflineEffectEvidenceSourceV2) http.Handler {
	r := chi.NewRouter()
	registerEtcdFleetOfflineEffectV2Routes(r, func(h http.Handler) http.Handler { return h }, store,
		offlineDeviceHTTPStore{platform: handler.FleetOfflineRunnerPlatform}, source)
	return r
}

func TestOfflineEffectV2RoutesDefaultOffWithoutIndependentSource(t *testing.T) {
	plan, attempt := uuid.NewString(), uuid.NewString()
	router := offlineEffectV2Router(&offlineEffectV2HTTPStore{}, nil)
	response := grantHTTPRequest(router, offlinePrincipal(), http.MethodPost,
		"/api/v1/fleet/offline/attempts/"+plan+"/"+attempt+"/effect-grants/v2", "{}")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "qualified independent evidence source") || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("default-off response=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body)
	}
}

func TestOfflineEffectV2OnlyDispatchResponseAuthorizes(t *testing.T) {
	plan, attempt, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store := &offlineEffectV2HTTPStore{grant: offline.EffectGrantV2{ID: grantID, PlanID: plan, AttemptID: attempt}}
	source := &offlineEffectV2HTTPSource{}
	router := offlineEffectV2Router(store, source)
	base := "/api/v1/fleet/offline/attempts/" + plan + "/" + attempt
	dispatch := grantHTTPRequest(router, offlinePrincipal(), http.MethodPost, base+"/effect-grants/"+grantID+"/dispatch", "{}")
	if dispatch.Code != http.StatusCreated || store.dispatchCalls != 1 || source.effectCalls != 1 || !strings.Contains(dispatch.Body.String(), `"authorizesEffect":true`) {
		t.Fatalf("dispatch=%d store=%d source=%d body=%s", dispatch.Code, store.dispatchCalls, source.effectCalls, dispatch.Body)
	}
	read := grantHTTPRequest(router, offlinePrincipal(), http.MethodGet, base+"/delete-dispatch-reservation", "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"authorizesEffect":false`) || strings.Contains(read.Body.String(), `"authorizesEffect":true`) {
		t.Fatalf("read=%d body=%s", read.Code, read.Body)
	}
	grantRead := grantHTTPRequest(router, offlinePrincipal(), http.MethodGet, base+"/effect-grants/v2", "")
	if grantRead.Code != http.StatusOK || !strings.Contains(grantRead.Body.String(), `"authorizesEffect":false`) {
		t.Fatalf("grant read=%d body=%s", grantRead.Code, grantRead.Body)
	}
}
