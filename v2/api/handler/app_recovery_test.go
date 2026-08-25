package handler

import (
	"net/http/httptest"
	"testing"
)

func TestAppOperationIdempotencyIsPrincipalScopedAndRequestBound(t *testing.T) {
	request := map[string]interface{}{"keep": 3}
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/apps/demo/snapshots/retention", nil)
	req.Header.Set("Idempotency-Key", "retry-1")
	firstKey, firstDigest, ok := appOperationIdempotency(recorder, req, AccessPrincipal{TokenID: "token-one"}, "demo", "app.snapshot-prune", request)
	if !ok {
		t.Fatal("expected idempotency key")
	}
	secondKey, secondDigest, _ := appOperationIdempotency(recorder, req, AccessPrincipal{TokenID: "token-two"}, "demo", "app.snapshot-prune", request)
	if firstKey == secondKey {
		t.Fatal("principal identity must scope idempotency")
	}
	if firstDigest != secondDigest {
		t.Fatal("the same request must retain its request digest")
	}
	_, changedDigest, _ := appOperationIdempotency(recorder, req, AccessPrincipal{TokenID: "token-one"}, "demo", "app.snapshot-prune", map[string]interface{}{"keep": 2})
	if firstDigest == changedDigest {
		t.Fatal("request changes must change the digest")
	}
}

func TestSnapshotTimestampContract(t *testing.T) {
	for _, value := range []string{"20260825T140000", "20000101T000000"} {
		if !snapshotTimestampPattern.MatchString(value) {
			t.Fatalf("expected %q to be accepted", value)
		}
	}
	for _, value := range []string{"../snapshot", "2026-08-25", "20260825T140000.dump", ""} {
		if snapshotTimestampPattern.MatchString(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
