package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEvaluateProductionReadinessReadyProfile(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	report := evaluateProductionReadiness(productionReadinessInput{
		ProductionProfile:    true,
		ExplicitAuth:         true,
		StrongControlToken:   true,
		LegacySigningRetired: true,
		ControlLoopback:      true,
		NomadReachable:       true,
		NomadACL:             true,
		NomadTLS:             true,
		NomadServers:         3,
		NomadClients:         2,
		ConsulReachable:      true,
		ConsulACL:            true,
		ConsulTLS:            true,
		ConsulServers:        3,
		DatabaseTLS:          true,
		DatabaseExternal:     true,
		DatabasePITR:         true,
		DatabaseReplicas:     1,
		ObservabilityReady:   true,
		AuditDurable:         true,
		AuditRetentionDays:   365,
	}, now)

	if report.Status != "ready" {
		t.Fatalf("status = %q, want ready; report=%+v", report.Status, report)
	}
	if report.Summary.Failed != 0 || report.Summary.Warnings != 1 {
		t.Fatalf("summary = %+v, want zero failures and one documented warning", report.Summary)
	}
	if report.Schema != productionReadinessSchema || report.GeneratedAt != now.Format(time.RFC3339) {
		t.Fatalf("unexpected contract metadata: %+v", report)
	}
}

func TestEvaluateProductionReadinessFailsClosed(t *testing.T) {
	report := evaluateProductionReadiness(productionReadinessInput{
		NomadServers:          1,
		NomadClients:          1,
		ConsulServers:         1,
		SecretProblems:        1,
		SecretMigrationItems:  1,
		UntrustedDeployments:  []string{"dirty-app"},
		StatefulApps:          1,
		MissingSnapshots:      []string{"stateful-app"},
		MissingOffsiteBackups: []string{"stateful-app"},
		MissingRecoveryDrills: []string{"database.restore"},
		ProbeErrors:           []string{"Nomad inspection failed"},
	}, time.Now().UTC())

	if report.Status != "blocked" || report.Summary.Failed == 0 {
		t.Fatalf("expected blocked report, got %+v", report)
	}
	assertReadinessCheckStatus(t, report, "control.explicit_auth", "fail")
	assertReadinessCheckStatus(t, report, "nomad.acl", "fail")
	assertReadinessCheckStatus(t, report, "consul.tls", "fail")
	assertReadinessCheckStatus(t, report, "secrets.strict", "fail")
	assertReadinessCheckStatus(t, report, "deployments.provenance", "fail")
	assertReadinessCheckStatus(t, report, "recovery.drills", "fail")
	assertReadinessCheckStatus(t, report, "inspection.complete", "fail")
}

func TestProductionReadinessHelpersAreValueSafeAndStrict(t *testing.T) {
	if !databaseUsesTLS("postgres://db.example/norn?sslmode=verify-full") {
		t.Fatal("verify-full should satisfy database TLS")
	}
	if databaseUsesTLS("postgres://localhost/norn?sslmode=disable") {
		t.Fatal("sslmode=disable must not satisfy database TLS")
	}
	if databaseUsesTLS("postgres://db.example/norn?sslmode=require") {
		t.Fatal("sslmode=require must not satisfy verified database TLS")
	}
	if !nestedBool(map[string]any{"TLSConfig": map[string]any{"EnableHTTP": true}}, "tlsconfig", "enablehttp") {
		t.Fatal("nested bool lookup should be case-insensitive")
	}
	if !databaseHostIsExternal("postgres://db.tailnet.example/norn?sslmode=verify-full") || databaseHostIsExternal("postgres://127.0.0.1/norn?sslmode=verify-full") {
		t.Fatal("database failure-domain classification is incorrect")
	}
	if !isLoopbackBind("::1") || isLoopbackBind("0.0.0.0") {
		t.Fatal("loopback bind classification is incorrect")
	}
}

func TestProductionReadinessRequiresExplicitReadPrincipal(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/production/readiness", nil)
	rec := httptest.NewRecorder()
	(&Handler{}).ProductionReadiness(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func assertReadinessCheckStatus(t *testing.T, report ProductionReadinessReport, id, status string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			if check.Status != status {
				t.Fatalf("check %s status = %q, want %q", id, check.Status, status)
			}
			return
		}
	}
	t.Fatalf("check %s not found", id)
}
