package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"norn/v2/api/pipeline"
)

func restartRequestApp(req *http.Request, app string) *http.Request {
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
}

func TestRestartAppUsesSignedAcceptanceAndDoesNotCallNomadInline(t *testing.T) {
	opStore := &recordingOperationStore{authority: "authority-1"}
	h := &Handler{operationStore: opStore, pipeline: &pipeline.Pipeline{OperationStore: opStore, RestartAvailability: func() bool { return true }}}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/restart", nil)
	req.Header.Set("Idempotency-Key", "restart-demo-1")
	req = withOperationAcceptanceRequestContext(req, operationAcceptanceRequestContext{ReceiptID: "receipt-1", RequestID: "request-1", Actor: verifiedOperationActor{Issuer: "authority-1/device", Subject: "device-1"}})
	rec := httptest.NewRecorder()
	h.RestartApp(rec, restartRequestApp(req, "demo"))
	if rec.Code != http.StatusAccepted || opStore.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, opStore.calls, rec.Body.String())
	}
	got := opStore.acceptance
	if got.Identity.Kind != "app.restart" || got.Identity.Resource != "demo" || got.Identity.Key != "restart-demo-1" || got.Operation.App != "demo" || got.Operation.Kind != "app.restart" {
		t.Fatalf("acceptance=%+v operation=%+v", got.Identity, got.Operation)
	}
	if got.Audit.RequestReceiptID != "receipt-1" || got.Fingerprint.Digest == "" || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/operations/") {
		t.Fatalf("audit=%+v fingerprint=%+v location=%q", got.Audit, got.Fingerprint, rec.Header().Get("Location"))
	}
}

func TestRestartAppFailsClosedWithoutAcceptanceContext(t *testing.T) {
	h := &Handler{operationStore: &recordingOperationStore{authority: "authority-1"}, pipeline: &pipeline.Pipeline{RestartAvailability: func() bool { return true }}}
	req := restartRequestApp(httptest.NewRequest(http.MethodPost, "/api/v1/apps/demo/restart", nil), "demo")
	req.Header.Set("Idempotency-Key", "restart-demo-1")
	rec := httptest.NewRecorder()
	h.RestartApp(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "operation_acceptance_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
