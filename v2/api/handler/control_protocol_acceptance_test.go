package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/model"
	"norn/v2/api/store"
)

type recordingOperationStore struct {
	authority    string
	authorityErr error
	acceptance   store.OperationAcceptance
	result       store.AcceptedOperation
	err          error
	calls        int
}

func (s *recordingOperationStore) Authority(context.Context) (string, error) {
	return s.authority, s.authorityErr
}

func (s *recordingOperationStore) Accept(_ context.Context, acceptance store.OperationAcceptance) (store.AcceptedOperation, error) {
	s.calls++
	s.acceptance = acceptance
	if s.result.Operation.ID == "" {
		s.result.Operation = acceptance.Operation
	}
	return s.result, s.err
}

func (s *recordingOperationStore) Resolve(context.Context, store.OperationRequestIdentity, store.RequestFingerprint) (store.AcceptedOperation, error) {
	return store.AcceptedOperation{}, errors.New("unexpected Resolve call")
}

func acceptedMaintenanceRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/platform/upgrades", nil)
	req.Header.Set("Idempotency-Key", key)
	req = WithAccessPrincipal(req, &AccessPrincipal{Scopes: []string{ScopePlatformOperate}})
	return withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{
		ReceiptID: "receipt-1", RequestID: "request-1",
		Actor: verifiedOperationActor{
			Issuer: "authority-1/device", Subject: "device-1", CredentialID: "token-2", DeviceID: "device-1",
			Source: "norn-device", Scopes: []string{ScopePlatformOperate},
		},
	})
}

func TestQueueMaintenanceUsesAtomicAcceptanceBoundary(t *testing.T) {
	opStore := &recordingOperationStore{authority: "authority-1"}
	h := &Handler{operationStore: opStore}
	req := acceptedMaintenanceRequest("upgrade-123")
	rec := httptest.NewRecorder()
	payload := map[string]interface{}{"ref": "abc123", "mode": "restart", "drainMode": "wait"}
	h.queueMaintenanceOperation(rec, req, "platform.upgrade", "abc123", "control-plane replacement", payload)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if opStore.calls != 1 {
		t.Fatalf("Accept calls=%d, want 1", opStore.calls)
	}
	got := opStore.acceptance
	if got.Identity.Authority != "authority-1" || got.Identity.Actor.Issuer != "authority-1/device" || got.Identity.Actor.Subject != "device-1" || got.Identity.Kind != "platform.upgrade" || got.Identity.Resource != "control-plane" || got.Identity.Key != "upgrade-123" {
		t.Fatalf("identity=%+v", got.Identity)
	}
	if got.Audit.RequestReceiptID != "receipt-1" || got.Audit.RequestID != "request-1" || got.Audit.CredentialID != "token-2" || got.Audit.DeviceID != "device-1" || got.Audit.Source != "norn-device" {
		t.Fatalf("audit=%+v", got.Audit)
	}
	if got.Fingerprint.Version != store.OperationRequestFingerprintVersion || len(got.Fingerprint.Digest) != 64 {
		t.Fatalf("fingerprint=%+v", got.Fingerprint)
	}
	if got.Operation.Status != model.OperationQueued {
		t.Fatalf("operation=%+v", got.Operation)
	}
	if _, legacyKey := got.Operation.Metadata["idempotencyKey"]; legacyKey {
		t.Fatal("new acceptance must not populate the legacy global idempotency metadata")
	}
	if rec.Header().Get("Location") != "/api/v1/operations/"+got.Operation.ID {
		t.Fatalf("Location=%q", rec.Header().Get("Location"))
	}
}

func TestQueueMaintenanceReplayReturnsOriginalOperation(t *testing.T) {
	opStore := &recordingOperationStore{
		authority: "authority-1",
		result:    store.AcceptedOperation{Replayed: true, Operation: model.Operation{ID: "original-operation", Kind: "platform.smoke", Status: model.OperationSucceeded}},
	}
	h := &Handler{operationStore: opStore}
	rec := httptest.NewRecorder()
	h.queueMaintenanceOperation(rec, acceptedMaintenanceRequest("smoke-123"), "platform.smoke", "", "read-only platform assurance", map[string]interface{}{})
	if rec.Code != http.StatusOK || rec.Header().Get("Location") != "/api/v1/operations/original-operation" || !strings.Contains(rec.Body.String(), "original-operation") {
		t.Fatalf("status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
}

func TestQueueMaintenanceReplayExpiryIsStableGone(t *testing.T) {
	opStore := &recordingOperationStore{authority: "authority-1", err: &store.AcceptanceExpiredError{}}
	h := &Handler{operationStore: opStore}
	rec := httptest.NewRecorder()
	h.queueMaintenanceOperation(rec, acceptedMaintenanceRequest("expired-key"), "platform.smoke", "", "read-only platform assurance", map[string]interface{}{})
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"code":"idempotency_window_expired"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQueueMaintenanceRequiresCallerIdempotencyAndReservedReceipt(t *testing.T) {
	opStore := &recordingOperationStore{authority: "authority-1"}
	h := &Handler{operationStore: opStore}

	missingKey := acceptedMaintenanceRequest("")
	rec := httptest.NewRecorder()
	h.queueMaintenanceOperation(rec, missingKey, "platform.smoke", "", "read-only", map[string]interface{}{})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("missing key status=%d body=%s", rec.Code, rec.Body.String())
	}

	missingReceipt := httptest.NewRequest(http.MethodPost, "/api/v1/platform/smoke", nil)
	missingReceipt.Header.Set("Idempotency-Key", "smoke-123")
	missingReceipt = WithAccessPrincipal(missingReceipt, &AccessPrincipal{Scopes: []string{ScopePlatformOperate}})
	rec = httptest.NewRecorder()
	h.queueMaintenanceOperation(rec, missingReceipt, "platform.smoke", "", "read-only", map[string]interface{}{})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "operation_acceptance_unavailable") {
		t.Fatalf("missing receipt status=%d body=%s", rec.Code, rec.Body.String())
	}
	if opStore.calls != 0 {
		t.Fatalf("Accept calls=%d, want 0", opStore.calls)
	}
}

func TestQueueMaintenanceFailsClosedForUnverifiedActor(t *testing.T) {
	opStore := &recordingOperationStore{authority: "authority-1"}
	h := &Handler{operationStore: opStore}
	req := acceptedMaintenanceRequest("smoke-123")
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{ReceiptID: "receipt-1", ActorErr: errOperationActorUnverified})
	rec := httptest.NewRecorder()
	h.queueMaintenanceOperation(rec, req, "platform.smoke", "", "read-only", map[string]interface{}{})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "operation_actor_ambiguous") || strings.Contains(rec.Body.String(), "operationId") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestQueueMaintenanceMapsAcceptanceFailuresWithoutDisclosingLegacyResult(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "fingerprint conflict", err: &store.AcceptanceConflictError{}, status: http.StatusConflict, code: "idempotency_key_reused"},
		{name: "legacy ambiguity", err: &store.LegacyReplayAmbiguousError{}, status: http.StatusConflict, code: "legacy_idempotency_ambiguous"},
		{name: "indeterminate", err: &store.AcceptanceIndeterminateError{}, status: http.StatusServiceUnavailable, code: "operation_acceptance_indeterminate"},
		{name: "signing", err: &store.AcceptanceSignatureError{}, status: http.StatusServiceUnavailable, code: "operation_acceptance_unavailable"},
		{name: "authority", err: &store.AcceptanceAuthorityError{}, status: http.StatusServiceUnavailable, code: "operation_acceptance_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opStore := &recordingOperationStore{authority: "authority-1", err: tt.err}
			h := &Handler{operationStore: opStore}
			rec := httptest.NewRecorder()
			h.queueMaintenanceOperation(rec, acceptedMaintenanceRequest("same-key"), "platform.smoke", "", "read-only", map[string]interface{}{})
			if rec.Code != tt.status || !strings.Contains(rec.Body.String(), tt.code) || strings.Contains(rec.Body.String(), "original-operation") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestOperationRequestFingerprintIsDeterministicAndCoversSemantics(t *testing.T) {
	base := store.OperationAcceptance{
		Identity:  store.OperationRequestIdentity{Authority: "authority-1", Actor: store.OperationActor{Issuer: "issuer", Subject: "subject"}, Kind: "platform.upgrade", Resource: "control-plane", Key: "key"},
		Operation: model.Operation{Kind: "platform.upgrade", Ref: "abc", Status: model.OperationQueued, Risk: "upgrade", Source: "control-api", Message: "queued", Payload: map[string]interface{}{"ref": "abc", "mode": "restart", "drainMode": "wait"}, MaxAttempts: 1},
		Audit:     store.AcceptanceAuditContext{Source: "test"},
		Semantics: map[string]interface{}{"ref": "abc", "mode": "restart", "drainMode": "wait"},
	}
	a, err := store.CanonicalOperationRequestFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Operation.Payload = map[string]interface{}{"drainMode": "wait", "mode": "restart", "ref": "abc"}
	reordered.Semantics = map[string]interface{}{"drainMode": "wait", "mode": "restart", "ref": "abc"}
	b, err := store.CanonicalOperationRequestFingerprint(reordered)
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Operation.Payload = map[string]interface{}{"drainMode": "force", "mode": "restart", "ref": "abc"}
	changed.Semantics = map[string]interface{}{"drainMode": "force", "mode": "restart", "ref": "abc"}
	c, err := store.CanonicalOperationRequestFingerprint(changed)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == c {
		t.Fatalf("fingerprints a=%+v b=%+v c=%+v", a, b, c)
	}
}
