package handler

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"norn/v2/api/config"
	"norn/v2/api/store"
)

func TestOperationStorePolicyErrorIsExposed(t *testing.T) {
	db := &store.DB{Pool: new(pgxpool.Pool)}
	h := New(db, nil, nil, nil, &config.Config{
		AuditSigningKey:  strings.Repeat("k", 32),
		ControlAuthority: "not-a-uuid",
	}, nil, nil, nil, nil, nil, nil)

	if !errors.Is(h.OperationStoreError(), store.ErrAcceptanceInvalid) {
		t.Fatalf("operation store error = %v, want acceptance validation error", h.OperationStoreError())
	}
	if h.OperationStore() != nil {
		t.Fatal("operation store initialized despite invalid policy")
	}
}
