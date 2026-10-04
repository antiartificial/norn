package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/controller"
)

type countingReconcilerStore struct{ lists int }

func (s *countingReconcilerStore) ReconcileFleetResource(context.Context, string, func(controller.Input) controller.Status, func()) (*controller.Resource, error) {
	return &controller.Resource{}, nil
}

func (s *countingReconcilerStore) ListFleetResourceNames(context.Context, string, int) ([]string, error) {
	s.lists++
	return nil, nil
}

func TestFleetReconcilerNotifyMiddleware(t *testing.T) {
	store := &countingReconcilerStore{}
	r := &controller.Reconciler{Store: store, EventRescanGap: time.Nanosecond}
	r.Step(context.Background()) // startup rescan
	status := http.StatusOK
	handler := fleetReconcilerNotify(r)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	do := func(method, path string) {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	}

	do(http.MethodGet, "/api/v1/fleet/resources/prod/observations")
	do(http.MethodPost, "/api/v1/apps/x/promote")
	status = http.StatusConflict
	do(http.MethodPost, "/api/v1/fleet/resources/prod/observations")
	if r.Pending() != 0 || store.lists != 1 {
		t.Fatalf("reads, other routes and refused mutations must not enqueue: pending=%d lists=%d", r.Pending(), store.lists)
	}

	status = http.StatusCreated
	do(http.MethodPost, "/api/v1/fleet/resources/prod/observations")
	if r.Pending() != 1 {
		t.Fatalf("an accepted resource mutation must enqueue that resource, pending=%d", r.Pending())
	}
	r.Step(context.Background())

	do(http.MethodPost, "/api/v1/fleet/plans/p1/attempts/a1/advance")
	time.Sleep(time.Millisecond)
	r.Step(context.Background())
	if store.lists != 2 {
		t.Fatalf("an accepted plan/attempt mutation must trigger a rescan, lists=%d", store.lists)
	}
}

func TestFleetReconcilerNotifyMiddlewareImplicitOK(t *testing.T) {
	r := &controller.Reconciler{Store: &countingReconcilerStore{}}
	silent := fleetReconcilerNotify(r)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	silent.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/fleet/resources/prod/observations", nil))
	if r.Pending() != 1 {
		t.Fatalf("a handler that never writes answers an implicit 200 and must still notify, pending=%d", r.Pending())
	}
}

func TestFleetReconcilerEnabledFlag(t *testing.T) {
	for value, want := range map[string]bool{"": true, "  ": true, "true": true, " TRUE ": true, "1": true, "false": false, " False ": false, "0": false} {
		got, err := fleetReconcilerEnabled(func(string) string { return value })
		if err != nil || got != want {
			t.Fatalf("%s=%q enabled=%t err=%v, want %t", fleetReconcilerEnv, value, got, err, want)
		}
	}
	for _, value := range []string{"flase", "yes", "off", "enabled"} {
		if _, err := fleetReconcilerEnabled(func(string) string { return value }); err == nil || !strings.Contains(err.Error(), fleetReconcilerEnv) {
			t.Fatalf("%s=%q must fail startup, err=%v", fleetReconcilerEnv, value, err)
		}
	}
}
