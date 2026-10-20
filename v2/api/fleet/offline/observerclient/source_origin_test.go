package observerclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type fakeProtectedSource struct {
	raw    []byte
	sha    string
	called int
}

func (f *fakeProtectedSource) ReadOfflineEffectSourceTranscript(_ context.Context, sha string) ([]byte, error) {
	f.called++
	f.sha = sha
	return f.raw, nil
}

func TestSourceOriginExactChallengeAndProtectedTranscript(t *testing.T) {
	start := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	b := offline.Binding{SchemaVersion: offline.BindingSchemaV2, PlanID: "11111111-1111-4111-8111-111111111111", TargetID: "target-1", Resource: offline.WorkResource,
		SourceCommit: strings.Repeat("a", 40), SavedPlanSHA256: strings.Repeat("b", 64), EffectSHA256: strings.Repeat("c", 64), CandidateSHA256: strings.Repeat("1", 64),
		Backend:             offline.Backend{Bucket: "bucket", Key: "state", VersionID: "version-1", Lineage: "lineage-1", Serial: 22, RawSHA256: strings.Repeat("d", 64)},
		ProviderAccountUUID: "work-account", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID, OwnerAuthorizationID: strings.Repeat("9", 64),
		OwnerEnvelopeSHA256: strings.Repeat("e", 64), RunnerPrincipalSubject: "offline-runner-1", DispatchNonceSHA256: strings.Repeat("f", 64), FenceGeneration: 29,
		CreatedAt: start, ExpiresAt: start.Add(10 * time.Minute)}
	for i, name := range []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		id := []string{"101", "102", offline.WorkDropletID, "104", "105", "106", "107", "108"}[i]
		b.ProviderNodes = append(b.ProviderNodes, offline.ProviderNode{Name: name, ProviderID: id})
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	bindingRaw, _ := json.Marshal(b)
	digest := sha256.Sum256(bindingRaw)
	a := offline.Attempt{ID: "22222222-2222-4222-8222-222222222222", PlanID: b.PlanID, TargetID: b.TargetID, BindingSHA256: hex.EncodeToString(digest[:]), FenceGeneration: b.FenceGeneration}
	c := offline.EffectChallengeV1{ID: "challenge-one", PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256, SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID}
	proof := map[string]any{"schema": "norn.fleet-offline-effect-source/v1", "repository": "antiartificial/norn-fleet", "branch": "main", "commit": b.SourceCommit, "protected": true, "validationRun": 42}
	raw, _ := json.Marshal(proof)
	f := &fakeProtectedSource{raw: raw}
	transcript, err := (SourceOrigin{Reader: f}).ReadSource(context.Background(), c, b, a)
	if err != nil || len(transcript) == 0 || f.sha != b.SourceCommit || f.called != 1 {
		t.Fatalf("valid source refused: %v", err)
	}
	var got map[string]any
	if json.Unmarshal(transcript, &got) != nil || got["challengeId"] != c.ID || got["bindingSHA256"] != a.BindingSHA256 {
		t.Fatal("transcript not bound")
	}
	for _, change := range []func(){func() { c.AttemptID = "other" }, func() { c.AttemptID = a.ID; proof["protected"] = false }, func() { proof["protected"] = true; proof["commit"] = strings.Repeat("b", 40) }} {
		change()
		f.raw, _ = json.Marshal(proof)
		if _, err := (SourceOrigin{Reader: f}).ReadSource(context.Background(), c, b, a); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	c.AttemptID = a.ID
	f.raw = []byte(`{"schema":"norn.fleet-offline-effect-source/v1","repository":"antiartificial/norn-fleet","branch":"main","commit":"` + b.SourceCommit + `","protected":true,"protected":false,"validationRun":42}`)
	if _, err := (SourceOrigin{Reader: f}).ReadSource(context.Background(), c, b, a); err == nil {
		t.Fatal("duplicate protected key accepted")
	}
}
