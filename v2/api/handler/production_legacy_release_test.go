package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"norn/v2/api/config"
)

func TestProductionLaneRefusesLegacyDeploymentIngress(t *testing.T) {
	h := &Handler{cfg: &config.Config{Environment: "production"}}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"direct deploy": h.Deploy,
		"webhook":       h.Webhook,
		"deploy group":  h.DeployGroup,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			call(rec, httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
			}
		})
	}
	rec := httptest.NewRecorder()
	h.ReplayWebhookDelivery(rec, httptest.NewRequest(http.MethodPost, "/api/webhooks/deliveries/id/replay", strings.NewReader("{\"mode\":\"deploy\"}")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("replay status = %d, want %d", rec.Code, http.StatusConflict)
	}
}
