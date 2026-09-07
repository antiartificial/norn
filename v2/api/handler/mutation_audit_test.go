package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

func TestMutationAuditDigestDetectsTampering(t *testing.T) {
	finished := time.Date(2026, 8, 22, 14, 0, 1, 0, time.UTC)
	event := store.MutationAuditEvent{
		ID: "audit-1", RequestID: "request-1", PrincipalSubject: "operator@example.test",
		TokenID: "token-1", DeviceID: "device-1", Scopes: []string{ScopeAPIWrite},
		Method: http.MethodPost, Path: "/api/apps/{id}/deploy", ClientIP: "100.64.0.10",
		UserAgent: "NornUI/1", Status: http.StatusAccepted, Outcome: "succeeded",
		StartedAt: finished.Add(-time.Second), FinishedAt: &finished, DurationMs: 1000,
	}
	key := strings.Repeat("k", 32)
	event.KeyID = auditKeyID(key)
	event.RecordDigest = signMutationAudit(key, event)
	if got := mutationAuditIntegrity(key, event); got != "verified" {
		t.Fatalf("integrity = %q, want verified", got)
	}
	event.Status = http.StatusInternalServerError
	if got := mutationAuditIntegrity(key, event); got != "invalid" {
		t.Fatalf("tampered integrity = %q, want invalid", got)
	}
}

func TestMutationAuditSupportsPreviousVerificationKey(t *testing.T) {
	finished := time.Now().UTC()
	oldKey := strings.Repeat("o", 32)
	event := store.MutationAuditEvent{ID: "audit-old", PrincipalSubject: "operator", Method: http.MethodPost, Path: "/api/apps/{id}/deploy", Status: 202, Outcome: "succeeded", StartedAt: finished.Add(-time.Second), FinishedAt: &finished, DurationMs: 1000, KeyID: auditKeyID(oldKey)}
	event.RecordDigest = signMutationAudit(oldKey, event)
	if got := mutationAuditIntegrityWithKeys([]string{strings.Repeat("n", 32), oldKey}, event); got != "verified" {
		t.Fatalf("integrity=%q", got)
	}
}

func TestMutationAuditIncidentDigestDetectsTamperingAndSupportsRotation(t *testing.T) {
	oldKey := strings.Repeat("o", 32)
	incident := store.MutationAuditIncident{
		ID: "incident-1", AuditEventID: "audit-1", ReasonCode: "legacy_timestamp_precision",
		Explanation:    "PostgreSQL truncated nanoseconds before the canonicalization fix.",
		AcknowledgedBy: "operator@example.test", AcknowledgedAt: time.Now().UTC(), KeyID: auditKeyID(oldKey),
	}
	incident.RecordDigest = signMutationAuditIncident(oldKey, incident)
	if got := mutationAuditIncidentIntegrityWithKeys([]string{strings.Repeat("n", 32), oldKey}, incident); got != "verified" {
		t.Fatalf("integrity=%q", got)
	}
	incident.Explanation = "tampered"
	if got := mutationAuditIncidentIntegrityWithKeys([]string{oldKey}, incident); got != "invalid" {
		t.Fatalf("tampered integrity=%q", got)
	}
}

func TestMutationAuditDigestSurvivesPostgresTimestampPrecision(t *testing.T) {
	started := time.Date(2026, 8, 22, 14, 0, 0, 123456789, time.UTC)
	finished := started.Add(987654321 * time.Nanosecond)
	key := strings.Repeat("k", 32)
	event := store.MutationAuditEvent{
		ID: "audit-postgres-precision", PrincipalSubject: "operator",
		Method: http.MethodPost, Path: "/api/v1/production/drills", Status: 201, Outcome: "succeeded",
		StartedAt: started, FinishedAt: &finished, DurationMs: finished.Sub(started).Milliseconds(),
		KeyID: auditKeyID(key),
	}
	event.RecordDigest = signMutationAudit(key, event)

	// Simulate the precision retained by a PostgreSQL timestamptz round-trip.
	event.StartedAt = event.StartedAt.Truncate(time.Microsecond)
	storedFinished := event.FinishedAt.Truncate(time.Microsecond)
	event.FinishedAt = &storedFinished
	if got := mutationAuditIntegrity(key, event); got != "verified" {
		t.Fatalf("database round-trip integrity = %q, want verified", got)
	}
}

func TestMutationAuditRedactsSecretKeyPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/apps/demo/secrets/API_TOKEN", nil)
	if got := mutationAuditPath(req); got != "/api/apps/demo/secrets/{key}" {
		t.Fatalf("path = %q", got)
	}
}

func TestMutationAuditExcludesBulkTelemetryIngress(t *testing.T) {
	for _, path := range []string{"/api/access/observations", "/api/access/cloudflare/logpush"} {
		if isAuditedMutation(httptest.NewRequest(http.MethodPost, path, nil)) {
			t.Fatalf("bulk telemetry path %s should not create one audit row per batch", path)
		}
	}
}

func TestProductionMutationAuditFailsClosedWithoutDatabase(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{Profile: "production", AuditSigningKey: strings.Repeat("k", 32)}, nil, nil, nil, nil, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", nil)
	rec := httptest.NewRecorder()
	h.MutationAuditMiddleware(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "mutation_audit_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestFleetAuthorityOnlyMutationAuditFailsClosedWithoutDatabase(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{Profile: "development", FleetAuthorityOnly: true, AuditSigningKey: strings.Repeat("k", 32)}, nil, nil, nil, nil, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/node-pools/app/plan", nil)
	rec := httptest.NewRecorder()
	h.MutationAuditMiddleware(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "mutation_audit_unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDevelopmentMutationAuditAllowsDatabaseFreeMode(t *testing.T) {
	h := New(nil, nil, nil, nil, &config.Config{Profile: "development"}, nil, nil, nil, nil, nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/api/apps/demo/deploy", nil)
	rec := httptest.NewRecorder()
	h.MutationAuditMiddleware(next).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
