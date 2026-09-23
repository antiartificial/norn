package store_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/store"
)

// TestControlExportCanonical verifies the canonical export preserves original
// audit digests, carries the format/schema versions, and is deterministic.
// Opt-in via NORN_TEST_DATABASE_URL.
func TestControlExportCanonical(t *testing.T) {
	db := connectForConformance(t)
	ctx := context.Background()
	for _, table := range []string{"mutation_audit_incidents", "mutation_audit_events", "exec_sessions", "step_up_challenges", "access_tokens", "access_devices", "access_grants", "access_enrollments"} {
		if _, err := db.Pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}

	// A finished audit event carries a record digest (the receipt bytes).
	auditID := uuid.NewString()
	if err := db.ReserveMutationAudit(ctx, &store.MutationAuditEvent{
		ID: auditID, PrincipalSubject: "op@example.com", Method: "POST",
		Path: "/api/v1/apps/web/deploy", Scopes: []string{"api:write"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishMutationAudit(ctx, auditID, "/api/v1/apps/web/deploy", 200, "success", time.Now(), 12, "digest-abc123"); err != nil {
		t.Fatal(err)
	}
	dev := &store.AccessDevice{ID: uuid.NewString(), Name: "laptop", CreatedAt: time.Now()}
	if err := db.CreateAccessDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAccessGrant(ctx, &store.AccessGrant{ID: uuid.NewString(), IP: "10.0.0.1", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	export, err := db.ExportControlState(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	if export.FormatVersion != store.ControlExportFormatVersion || export.SchemaVersion != store.SchemaVersion {
		t.Fatalf("versions wrong: format=%d schema=%d", export.FormatVersion, export.SchemaVersion)
	}
	if export.Truncated {
		t.Fatal("small export should not be truncated")
	}
	if len(export.AuditEvents) != 1 || export.AuditEvents[0].RecordDigest != "digest-abc123" {
		t.Fatalf("audit digest not preserved: %+v", export.AuditEvents)
	}
	if len(export.Devices) != 1 || len(export.Grants) != 1 {
		t.Fatalf("auth state missing: devices=%d grants=%d", len(export.Devices), len(export.Grants))
	}

	// The digest survives into the canonical JSON bytes.
	raw, err := export.MarshalCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"recordDigest": "digest-abc123"`) {
		t.Fatal("canonical JSON dropped the record digest")
	}

	// Deterministic: same state, same bytes.
	again, err := db.ExportControlState(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	raw2, _ := again.MarshalCanonical()
	if !bytes.Equal(raw, raw2) {
		t.Fatal("export is not deterministic for identical state")
	}
}
