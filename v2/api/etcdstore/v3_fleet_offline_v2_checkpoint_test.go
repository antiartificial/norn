package etcdstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"norn/v2/api/fleet/offline"
)

func TestOfflineV2SignedTopologyCheckpointRestore(t *testing.T) {
	h, targetID := offlineTestHarness(t)
	now := time.Now().UTC().Truncate(time.Second)
	planID := uuid.NewString()
	offlineTestPlan(h, planID)
	b := offlineTestBinding(planID, targetID, now)
	b.SchemaVersion = offline.BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("1", 64)
	for i, name := range []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		id := string(rune('1' + i))
		if name == offline.WorkNode {
			id = offline.WorkDropletID
		}
		b.ProviderNodes = append(b.ProviderNodes, offline.ProviderNode{Name: name, ProviderID: id})
	}
	b, approval := offlineTestApproval(t, b, now)
	if _, err := h.s.ReserveOfflineDispatch(h.ctx(), b, approval, now); err != nil {
		t.Fatal(err)
	}
	attempt, err := h.s.AdmitOfflineAttempt(h.ctx(), offlineAttemptClaim(b), offlineTestRunnerIdentity(b.RunnerPrincipalSubject), uuid.NewString(), now)
	if err != nil {
		t.Fatal(err)
	}
	nodes := []map[string]string{}
	for _, row := range b.ProviderNodes {
		nodes = append(nodes, map[string]string{"name": row.Name, "providerId": row.ProviderID, "status": "active"})
	}
	observed := now.Add(-time.Minute)
	receipt := map[string]any{"schemaVersion": offline.PrechangeSchema, "observedAt": observed, "readOnly": true, "authorizesEffect": false,
		"plan":      map[string]any{"candidateSHA256": b.CandidateSHA256, "source": map[string]any{"commit": b.SourceCommit, "root": b.Resource, "lockfileSHA256": strings.Repeat("2", 64)}, "provider": map[string]any{"address": "registry.opentofu.org/digitalocean/digitalocean", "version": "2.100.0", "archiveSHA256": "4dd5b6e8c1be5fb619812fe738d0b49e0d24731fb290c0ab5190b7bc60c70e66"}, "savedPlanSHA256": b.SavedPlanSHA256, "effectSHA256": b.EffectSHA256, "tofuVersion": "1.10.6"},
		"freshness": map[string]any{"schemaVersion": "norn.fleet-offline-contraction-freshness/v1", "observedAt": observed.Add(-time.Second), "candidateSHA256": b.CandidateSHA256, "accountUUID": b.ProviderAccountUUID, "vpcUUID": "b9d7d407-cdf9-4f1e-8a44-8983812860a8", "backend": b.Backend, "nodes": nodes, "readOnly": true, "authorizesEffect": false},
		"workload":  map[string]any{"job": "hello-norn-mysql", "namespace": "default", "runningAllocationIDs": []string{"alloc1", "alloc2"}, "survivingClientNames": []string{"app-01", "app-02"}}}
	raw, _ := json.Marshal(receipt)
	hash := sha256.Sum256(raw)
	checkpoint, err := h.s.RecordOfflineCheckpoint(h.ctx(), planID, attempt.ID, offlineTestRunnerIdentity(b.RunnerPrincipalSubject), "prechange_observed", hex.EncodeToString(hash[:]), 0, now, raw)
	if err != nil || checkpoint.Prechange == nil || !checkpoint.Prechange.ProviderTopologyVerified {
		t.Fatalf("v2 checkpoint: %+v %v", checkpoint, err)
	}
	stored, err := h.s.GetOfflineCheckpoint(h.ctx(), planID)
	if err != nil || stored.Prechange == nil || !stored.Prechange.ProviderTopologyVerified {
		t.Fatalf("v2 restore: %+v %v", stored, err)
	}
	// A restored checkpoint cannot keep its matched-topology claim if the
	// immutable attempt's reservation digest has been corrupted.
	attempt.BindingSHA256 = strings.Repeat("0", 64)
	encoded, _ := json.Marshal(attempt)
	if _, err := h.s.kv.Put(h.ctx(), h.s.offlineAttemptKey(planID), string(encoded)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.GetOfflineCheckpoint(h.ctx(), planID); !errors.Is(err, ErrOfflineAttemptCorrupt) {
		t.Fatalf("corrupt attempt accepted: %v", err)
	}
}
