package store

import "norn/v2/api/hub"

// The event boundary for Norn v3 (roadmap M1 / P5, "revisioned event cursors")
// is already seamed the ports-and-adapters way: the hub package owns the
// EventStore interface it consumes (a consumer-defined port), and *DB is its
// PostgreSQL adapter. Because store already imports hub, the port lives in hub
// rather than here to avoid an import cycle.
//
// This assertion makes the adapter/port contract explicit and compile-checked
// (previously it was only enforced where hub.SetStore is called). The shared
// behavioral contract — monotonic append cursors, ordered/limited tail reads,
// retention bounds — lives in event_store_conformance_test.go and is what a
// future etcd adapter (mapping cursors to revisions under an authority epoch)
// must satisfy.
var _ hub.EventStore = (*DB)(nil)
