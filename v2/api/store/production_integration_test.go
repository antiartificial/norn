package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestProductionEvidenceLifecycle validates the PostgreSQL transaction and
// migration behavior for the production audit and recovery-drill tables. It is
// opt-in so normal unit tests never mutate a developer database.
func TestProductionEvidenceLifecycle(t *testing.T) {
	databaseURL := os.Getenv("NORN_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("NORN_TEST_DATABASE_URL is not set")
	}
	db, err := Connect(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	auditID := uuid.NewString()
	oldAuditID := uuid.NewString()
	drillID := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM mutation_audit_events WHERE id=$1`, auditID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM mutation_audit_events WHERE id=$1`, oldAuditID)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM recovery_drills WHERE id=$1`, drillID)
	})

	audit := &MutationAuditEvent{
		ID: auditID, RequestID: "integration-request", PrincipalSubject: "integration-operator",
		Scopes: []string{"admin"}, Method: "POST", Path: "/api/v1/production/drills",
		Outcome: "started", StartedAt: now, KeyID: "integration-key",
	}
	if err := db.ReserveMutationAudit(ctx, audit); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Second)
	if err := db.FinishMutationAudit(ctx, auditID, audit.Path, 201, "succeeded", finished, 1000, "integration-digest"); err != nil {
		t.Fatal(err)
	}
	events, err := db.ListMutationAudits(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	foundAudit := false
	for _, event := range events {
		if event.ID == auditID {
			foundAudit = event.Status == 201 && event.Outcome == "succeeded" && event.RecordDigest == "integration-digest"
		}
	}
	if !foundAudit {
		t.Fatal("completed mutation audit receipt was not persisted")
	}
	oldStarted := now.AddDate(-2, 0, 0)
	oldAudit := &MutationAuditEvent{ID: oldAuditID, PrincipalSubject: "retention-test", Method: "POST", Path: "/api/test", Outcome: "started", StartedAt: oldStarted}
	if err := db.ReserveMutationAudit(ctx, oldAudit); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishMutationAudit(ctx, oldAuditID, oldAudit.Path, 200, "succeeded", oldStarted.Add(time.Second), 1000, "old-digest"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := db.PruneMutationAudits(ctx, now.AddDate(-1, 0, 0)); err != nil || deleted < 1 {
		t.Fatalf("audit prune deleted=%d err=%v", deleted, err)
	}

	drill := &RecoveryDrill{
		ID: drillID, Kind: "database.restore", Target: "integration-sandbox", Status: "running",
		InitiatedBy: "integration-operator", Evidence: map[string]string{}, StartedAt: now,
	}
	if err := db.InsertRecoveryDrill(ctx, drill); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRecoveryDrill(ctx, drillID, "passed", map[string]string{"receipt": "integration"}, finished); err != nil {
		t.Fatal(err)
	}
	latest, err := db.LatestPassedRecoveryDrills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := latest["database.restore"]; got.Before(now) {
		t.Fatalf("latest restore drill = %s", got)
	}
}
