package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCronTriggerReconciliationRequiresOperatorScopeAndPositiveEvidence(t *testing.T) {
	h := &Handler{}
	serve := func(scopes []string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/widget/operations/source/cron-trigger-reconciliation", bytes.NewBufferString(body))
		if scopes != nil {
			req = WithAccessPrincipal(req, &AccessPrincipal{Subject: "operator", Scopes: scopes})
		}
		rec := httptest.NewRecorder()
		h.QueueCronTriggerReconciliation(rec, req)
		return rec
	}
	if rec := serve(nil, `{"effectId":"effect","evalId":"eval","confirm":true}`); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("missing scope status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopeAPIRead}, `{"effectId":"effect","evalId":"eval","confirm":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only scope status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopeAPIWrite}, `{"effectId":"effect","evalId":"eval"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing confirmation status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := serve([]string{ScopeAPIWrite}, `{"effectId":"effect","confirm":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing evaluation status=%d body=%s", rec.Code, rec.Body.String())
	}
}
