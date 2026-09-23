package memstore_test

import (
	"testing"

	"norn/v2/api/hub"
	"norn/v2/api/memstore"
	"norn/v2/api/store"
	"norn/v2/api/store/storetest"
)

// TestEventStoreConformance_Memory runs the shared event-store conformance suite
// against the in-memory adapter. No database required.
func TestEventStoreConformance_Memory(t *testing.T) {
	storetest.RunEventStoreConformance(t, func(t *testing.T) hub.EventStore {
		return memstore.NewEventStore()
	})
}

// TestOperationStoreConformance_Memory runs the shared operation-store
// conformance suite — the hardest boundary (claim exclusivity, lease fencing,
// idempotency, interrupted-operation recovery) — against the in-memory adapter.
// No database required, so it proves in ordinary CI that these fencing
// invariants are backend-neutral, not PostgreSQL-specific.
func TestOperationStoreConformance_Memory(t *testing.T) {
	storetest.RunOperationStoreConformance(t, func(t *testing.T) store.OperationStore {
		return memstore.NewOperationStore()
	})
}
