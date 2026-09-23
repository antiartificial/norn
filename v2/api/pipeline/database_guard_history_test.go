package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/database"
	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

// guardHistory writes deploy/baseline history for one app with strictly
// increasing start times, in the order given.
type guardHistory struct {
	t    *testing.T
	p    *Pipeline
	app  string
	next time.Time
}

func guardTarget(service, binding string) database.TargetIdentity {
	return database.TargetIdentity{ServiceID: service, ServiceGeneration: 1, BindingID: binding, BindingGeneration: 1, Engine: database.EnginePostgreSQL, Database: "shop", Role: "shop"}
}

func namedPayload(t *testing.T, targets map[string]database.TargetIdentity) map[string]interface{} {
	t.Helper()
	set := recordedTargetSet{Schema: recordedTargetSetSchema, ProfileID: "mini", CatalogRevision: 1}
	for name, target := range targets {
		set.Targets = append(set.Targets, recordedNamedTarget{Name: name, Target: target})
	}
	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]interface{}{databaseTargetsPayloadKey: string(encoded)}
}

func (h *guardHistory) add(kind string, status model.OperationStatus, payload, metadata map[string]interface{}) string {
	h.t.Helper()
	h.next = h.next.Add(time.Second)
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	op := &model.Operation{ID: uuid.NewString(), App: h.app, Kind: kind, Status: status, SagaID: uuid.NewString(), Payload: payload, Metadata: metadata, Attempts: 1, MaxAttempts: 2, StartedAt: h.next, NextAttemptAt: h.next}
	ctx := context.Background()
	if status.Terminal() {
		if err := h.p.DB.InsertCompletedOperation(ctx, op); err != nil {
			h.t.Fatal(err)
		}
		return op.ID
	}
	queued := *op
	queued.Status = model.OperationQueued
	if err := h.p.DB.InsertOperation(ctx, &queued); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.p.DB.Pool.Exec(ctx, `UPDATE operations SET status=$2, started_at=$3 WHERE id=$1`, op.ID, status, h.next); err != nil {
		h.t.Fatal(err)
	}
	return op.ID
}

func guardCheck(p *Pipeline, app string, targets map[string]database.TargetIdentity) error {
	next := []recordedNamedTarget{}
	for name, target := range targets {
		next = append(next, recordedNamedTarget{Name: name, Target: target})
	}
	return p.requireRunningTargetsUnchanged(context.Background(), app, "", next)
}

func TestTargetGuardDistinguishesAdditionFromReplacementAndFailsClosedOnAmbiguity(t *testing.T) {
	p, _, _ := acceptancePipelineFixture(t)
	oldTarget, newTarget, other := guardTarget("pg-old", "primary-old"), guardTarget("pg-new", "primary-new"), guardTarget("pg-other", "analytics")
	history := func() *guardHistory {
		return &guardHistory{t: t, p: p, app: "guard-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8], next: time.Now().Add(-time.Hour)}
	}
	refused := func(name string, err error, ambiguous bool) {
		t.Helper()
		var targetErr *DatabaseTargetError
		if !errors.As(err, &targetErr) || targetErr.Ambiguous != ambiguous {
			t.Fatalf("%s: err = %v (want ambiguous=%v)", name, err, ambiguous)
		}
	}

	// Baseline: a succeeded deploy wrote primary on the old target.
	h := history()
	h.add("app.deploy", model.OperationSucceeded, namedPayload(t, map[string]database.TargetIdentity{"primary": oldTarget}), nil)
	if err := guardCheck(p, h.app, map[string]database.TargetIdentity{"primary": oldTarget}); err != nil {
		t.Fatalf("unchanged rollout = %v", err)
	}
	refused("same-name move", guardCheck(p, h.app, map[string]database.TargetIdentity{"primary": newTarget}), false)
	refused("rename onto a new target", guardCheck(p, h.app, map[string]database.TargetIdentity{"replacement": newTarget}), false)
	if err := guardCheck(p, h.app, map[string]database.TargetIdentity{"renamed": oldTarget}); err != nil {
		t.Fatalf("pure rename (same target) = %v", err)
	}
	if err := guardCheck(p, h.app, map[string]database.TargetIdentity{"primary": oldTarget, "analytics": other}); err != nil {
		t.Fatalf("independent addition = %v", err)
	}
	refused("replacement hidden beside an addition", guardCheck(p, h.app, map[string]database.TargetIdentity{"analytics": other, "replacement": newTarget}), false)

	// A failed partial rollout after the baseline may have left writers on
	// its targets; a writer-free failure (before migration) did not.
	h.add("app.deploy", model.OperationFailed, namedPayload(t, map[string]database.TargetIdentity{"primary": newTarget}), map[string]interface{}{"step": "healthy"})
	refused("after a partial rollout to another target", guardCheck(p, h.app, map[string]database.TargetIdentity{"primary": oldTarget}), false)
	h2 := history()
	h2.add("app.deploy", model.OperationSucceeded, namedPayload(t, map[string]database.TargetIdentity{"primary": oldTarget}), nil)
	h2.add("app.deploy", model.OperationFailed, namedPayload(t, map[string]database.TargetIdentity{"primary": newTarget}), map[string]interface{}{"step": "build"})
	if err := guardCheck(p, h2.app, map[string]database.TargetIdentity{"primary": oldTarget}); err != nil {
		t.Fatalf("after a writer-free failure = %v", err)
	}
	// A deploy that is still running (e.g. an active canary) counts too.
	h2.add("app.deploy", model.OperationRunning, namedPayload(t, map[string]database.TargetIdentity{"primary": newTarget}), nil)
	refused("beside a running deploy on another target", guardCheck(p, h2.app, map[string]database.TargetIdentity{"primary": oldTarget}), false)
	// A failure without step evidence is not assumed writer-free.
	h3 := history()
	h3.add("app.deploy", model.OperationSucceeded, namedPayload(t, map[string]database.TargetIdentity{"primary": oldTarget}), nil)
	h3.add("app.deploy", model.OperationFailed, map[string]interface{}{}, map[string]interface{}{"manualRecoveryRequired": true})
	refused("failure without step or targets", guardCheck(p, h3.app, map[string]database.TargetIdentity{"primary": oldTarget}), true)
	// Unknown newer history does not hide the known older writer: moving it is
	// a definite refusal, not an ambiguity a baseline could override.
	refused("known move behind unknown history", guardCheck(p, h3.app, map[string]database.TargetIdentity{"primary": newTarget}), false)
	// A partial rollout with known targets, then an unknown failure, then a
	// later check: both the known conflict and the ambiguity are retained.
	h3.add("app.deploy", model.OperationFailed, namedPayload(t, map[string]database.TargetIdentity{"primary": newTarget}), map[string]interface{}{"step": "healthy"})
	h3.add("app.deploy", model.OperationFailed, map[string]interface{}{}, nil)
	refused("known partial rollout behind unknown history", guardCheck(p, h3.app, map[string]database.TargetIdentity{"primary": oldTarget}), false)

	// Legacy-to-named: a legacy (unrecorded) succeeded deploy is ambiguous
	// until an explicit baseline is recorded; the baseline then governs.
	h4 := history()
	h4.add("app.deploy", model.OperationSucceeded, map[string]interface{}{"app": "legacy"}, nil)
	refused("legacy history", guardCheck(p, h4.app, map[string]database.TargetIdentity{"primary": oldTarget}), true)
	h4.add(DatabaseBaselineKind, model.OperationSucceeded, namedPayload(t, map[string]database.TargetIdentity{"primary": oldTarget}), nil)
	if err := guardCheck(p, h4.app, map[string]database.TargetIdentity{"primary": oldTarget}); err != nil {
		t.Fatalf("after a baseline = %v", err)
	}
	refused("move after a baseline", guardCheck(p, h4.app, map[string]database.TargetIdentity{"primary": newTarget}), false)

	// No history: absence of writers must be proven by the runtime.
	fresh := history()
	refused("no history without Nomad", guardCheck(p, fresh.app, map[string]database.TargetIdentity{"primary": oldTarget}), true)
	registered := map[string]bool{"guard-registered": true}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if registered[strings.TrimPrefix(r.URL.Path, "/v1/job/")] {
			_, _ = w.Write([]byte(`{"ID":"guard-registered"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	client, err := nomad.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	p.Nomad = client
	if err := guardCheck(p, fresh.app, map[string]database.TargetIdentity{"primary": oldTarget}); err != nil {
		t.Fatalf("fresh app with no registered job = %v", err)
	}
	refused("unrecorded registered job", guardCheck(p, "guard-registered", map[string]database.TargetIdentity{"primary": oldTarget}), true)
}
