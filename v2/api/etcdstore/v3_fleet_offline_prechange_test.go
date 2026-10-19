package etcdstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

func TestOfflinePrechangeRestoreRejectsCorruptSummary(t *testing.T) {
	when := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	plan := "11111111-1111-4111-8111-111111111111"
	checkpoint := offline.Checkpoint{SchemaVersion: offline.CheckpointSchema, PlanID: plan,
		AttemptID: "22222222-2222-4222-8222-222222222222", Sequence: 1,
		Phase: "prechange_observed", EvidenceSHA256: strings.Repeat("a", 64),
		AuthorityEpoch: 1, FenceGeneration: 29, RecordedAt: when,
		Prechange: &offline.PrechangeSummary{SchemaVersion: offline.PrechangeSchema,
			SHA256: strings.Repeat("a", 64), ObservedAt: when.Add(-time.Minute),
			ExpiresAt: when.Add(4 * time.Minute)}}
	raw, _ := json.Marshal(checkpoint)
	if _, err := decodeOfflineCheckpointPair(raw, raw, plan); err != nil {
		t.Fatal(err)
	}
	corrupt := checkpoint
	bad := *checkpoint.Prechange
	bad.SHA256 = strings.Repeat("b", 64)
	corrupt.Prechange = &bad
	badRaw, _ := json.Marshal(corrupt)
	if _, err := decodeOfflineCheckpointPair(badRaw, badRaw, plan); err == nil {
		t.Fatal("corrupt receipt hash accepted")
	}
	if _, err := decodeOfflineCheckpointPair(raw, badRaw, plan); err == nil {
		t.Fatal("head/event mismatch accepted")
	}
}
