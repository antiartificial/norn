package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"norn/v2/api/model"
)

func TestFirstPrivateServiceInstanceURLRejectsPublicTargets(t *testing.T) {
	service := model.ServiceManifestEntry{Instances: []model.ServiceInstance{
		{Address: "203.0.113.10", Port: 7701},
		{Address: "metadata.google.internal", Port: 80},
		{Address: "192.168.4.124", Port: 7701},
	}}
	if got := firstPrivateServiceInstanceURL(service); got != "http://192.168.4.124:7701" {
		t.Fatalf("internal service URL = %q", got)
	}
	service.Instances = []model.ServiceInstance{{Address: "100.64.12.3", Port: 7701}}
	if got := firstPrivateServiceInstanceURL(service); got != "http://100.64.12.3:7701" {
		t.Fatalf("tailnet service URL = %q", got)
	}
	service.Instances = []model.ServiceInstance{{Address: "203.0.113.10", Port: 7701}}
	if got := firstPrivateServiceInstanceURL(service); got != "" {
		t.Fatalf("public target must be rejected, got %q", got)
	}
}

func TestContextDBRollbackFailsClosedBeforeServiceDiscovery(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/ops/contextdb/feedback/event-123/rollback", nil)
	record := httptest.NewRecorder()
	(&Handler{}).ContextDBRollbackFeedback(record, request)
	if record.Code != http.StatusNotImplemented {
		t.Fatalf("rollback status = %d, want fail-closed 501", record.Code)
	}
}
