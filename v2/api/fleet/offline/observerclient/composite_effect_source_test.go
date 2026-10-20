package observerclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type compositeOriginStub struct{ raw []byte }

func (s compositeOriginStub) ReadSource(context.Context, offline.EffectChallengeV1, offline.Binding, offline.Attempt) ([]byte, error) {
	return s.raw, nil
}

type compositeProviderStub struct{ raw []byte }

func (s compositeProviderStub) Observe(context.Context, offline.Binding) ([]byte, error) {
	return s.raw, nil
}

type compositeBackendStub struct{ raw []byte }

func (s compositeBackendStub) Read(context.Context, offline.EffectChallengeV1, offline.Binding) ([]byte, error) {
	return s.raw, nil
}

type compositeWorkloadStub struct {
	value offline.EffectWorkloadObservation
}

func (s compositeWorkloadStub) Observe(_ context.Context, _ time.Time, _, _ string) (offline.EffectWorkloadObservation, error) {
	return s.value, nil
}

func compositeFixture(t *testing.T) (CompositeEffectSource, offline.EffectChallengeV1, offline.Binding, offline.Attempt, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	state := []byte(`{"version":4,"lineage":"lineage-1","serial":22}`)
	b := offline.Binding{SchemaVersion: offline.BindingSchemaV2, PlanID: "11111111-1111-4111-8111-111111111111", TargetID: "target-1", Resource: offline.WorkResource,
		SourceCommit: strings.Repeat("a", 40), SavedPlanSHA256: strings.Repeat("b", 64), EffectSHA256: strings.Repeat("c", 64), CandidateSHA256: strings.Repeat("1", 64),
		Backend:             offline.Backend{Bucket: "bucket", Key: "state", VersionID: "version-1", Lineage: "lineage-1", Serial: 22, RawSHA256: digestEffectBytes(state)},
		ProviderAccountUUID: offline.WorkProviderAccountUUID, NodeName: offline.WorkNode, DropletID: offline.WorkDropletID, OwnerAuthorizationID: strings.Repeat("9", 64),
		OwnerEnvelopeSHA256: strings.Repeat("e", 64), RunnerPrincipalSubject: "offline-runner-1", DispatchNonceSHA256: strings.Repeat("f", 64), FenceGeneration: 29,
		CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute)}
	for i, name := range []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		id := []string{"101", "102", offline.WorkDropletID, "104", "105", "106", "107", "108"}[i]
		b.ProviderNodes = append(b.ProviderNodes, offline.ProviderNode{Name: name, ProviderID: id})
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	br, _ := json.Marshal(b)
	a := offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: "22222222-2222-4222-8222-222222222222", PlanID: b.PlanID, BindingSHA256: digestEffectBytes(br), PrincipalSubject: b.RunnerPrincipalSubject,
		PrincipalTokenID: "token", PrincipalDeviceID: "device", TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, Status: "queued", CurrentPhase: "prechange_verified", CreatedAt: now.Add(-30 * time.Second)}
	pub, _, _ := ed25519.GenerateKey(nil)
	c, err := offline.NewEffectChallengeV1(offline.EffectIssuePurpose, b, a, 2, "observer", "observer-key", 7, pub, bytes.Repeat([]byte{1}, 32), nil, now.Add(-10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	source, _ := json.Marshal(map[string]any{"schema": "norn.fleet-offline-effect-source-observation/v1", "challengeId": c.ID, "planId": b.PlanID, "attemptId": a.ID, "bindingSHA256": a.BindingSHA256, "observedAt": now,
		"githubProof": map[string]any{"schema": "norn.fleet-offline-effect-source/v1", "repository": "antiartificial/norn-fleet", "branch": "main", "commit": b.SourceCommit, "protected": true, "validationRun": 42}})
	nodes := make([]any, 0, 8)
	for i, n := range b.ProviderNodes {
		id := uint64(101 + i)
		if n.Name == offline.WorkNode {
			id = 610327215
		}
		nodes = append(nodes, map[string]any{"id": id, "name": "tk-staging-nyc3-" + n.Name, "status": "active", "vpc_uuid": offline.WorkVPCUUID, "privateIP": "10.43.0.13", "publicIP": "159.203.100.16", "tags": []string{"norn:tk-staging-nyc3"}})
	}
	reads := make([]any, 19)
	for i := range reads {
		path := "/v2/account"
		if i == 1 || i == 18 {
			path = "/v2/projects/" + offline.WorkProjectUUID + "/resources?per_page=200&page=1"
		}
		if i >= 2 && i <= 17 {
			path = "/v2/droplets/" + b.ProviderNodes[(i-2)%8].ProviderID
		}
		reads[i] = map[string]any{"path": path, "status": 200, "rawSHA256": strings.Repeat("a", 64), "fields": map[string]any{"ok": true}}
	}
	provider, _ := json.Marshal(map[string]any{"schemaVersion": "norn.fleet-provider-effect-transcript/v1", "accountUUID": b.ProviderAccountUUID, "projectId": offline.WorkProjectUUID, "vpcUUID": offline.WorkVPCUUID, "reads": reads, "nodes": nodes})
	backend, _ := json.Marshal(map[string]any{"schema": "norn.fleet-effect-backend-transcript/v1", "challengeId": c.ID, "providerAccountUUID": b.ProviderAccountUUID, "backend": b.Backend, "rawState": state, "versioningAndLatest": "verified-before-and-after", "openTofuLock": "authenticated-absent-before-and-after"})
	wraw, _ := json.Marshal(map[string]any{"schema": "norn.fleet-effect-workload-reads/v2", "targetNodeId": WorkApp03NomadNodeID, "targetPrivateIp": "10.43.0.13", "observedAt": now, "snapshotSHA256": strings.Repeat("4", 64), "reads": []any{map[string]any{"path": "/v1/nodes"}}})
	w := offline.EffectWorkloadObservation{WorkloadEvidence: offline.WorkloadEvidence{ObservedAt: now, SHA256: strings.Repeat("4", 64), AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}, EvidenceSHA256: digestEffectBytes(wraw), RawEvidence: wraw}
	s := CompositeEffectSource{Origin: compositeOriginStub{source}, Provider: compositeProviderStub{provider}, Backend: compositeBackendStub{backend}, Workload: compositeWorkloadStub{w}, Clock: func() time.Time { return now }, NomadNodeID: WorkApp03NomadNodeID}
	return s, c, b, a, now
}

func TestCompositeEffectSourceBindsFourIndependentTranscripts(t *testing.T) {
	s, c, b, a, now := compositeFixture(t)
	r, err := s.ObserveEffect(context.Background(), c, b, a)
	if err != nil || len(r.Source) == 0 || len(r.Provider) == 0 || len(r.Backend) == 0 || len(r.Workload) == 0 || offline.ValidateEffectObservation(r.Observation, b, a, c.AuthorityEpoch, now) != nil {
		t.Fatalf("valid composition: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*CompositeEffectSource, *offline.EffectChallengeV1, *offline.Binding, *offline.Attempt)
	}{
		{"nomad pin", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.NomadNodeID = "caller-selected"
		}},
		{"provider identity", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Provider = compositeProviderStub{bytes.ReplaceAll(s.Provider.(compositeProviderStub).raw, []byte("10.43.0.13"), []byte("10.43.0.99"))}
		}},
		{"state digest", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Backend = compositeBackendStub{bytes.Replace(s.Backend.(compositeBackendStub).raw, []byte("lineage-1"), []byte("lineage-X"), 1)}
		}},
		{"source branch", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Origin = compositeOriginStub{bytes.Replace(s.Origin.(compositeOriginStub).raw, []byte(`"branch":"main"`), []byte(`"branch":"other"`), 1)}
		}},
		{"duplicate source key", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Origin = compositeOriginStub{bytes.Replace(s.Origin.(compositeOriginStub).raw, []byte(`"protected":true`), []byte(`"protected":true,"protected":false`), 1)}
		}},
		{"stale source", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Origin = compositeOriginStub{bytes.Replace(s.Origin.(compositeOriginStub).raw, []byte(now.Format(time.RFC3339)), []byte(now.Add(-31*time.Second).Format(time.RFC3339)), 1)}
		}},
		{"workload target", func(s *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			w := s.Workload.(compositeWorkloadStub).value
			w.RawEvidence = bytes.Replace(w.RawEvidence, []byte(WorkApp03NomadNodeID), []byte("other-node"), 1)
			w.EvidenceSHA256 = digestEffectBytes(w.RawEvidence)
			s.Workload = compositeWorkloadStub{w}
		}},
		{"expired minute", func(s *CompositeEffectSource, c *offline.EffectChallengeV1, _ *offline.Binding, _ *offline.Attempt) {
			s.Clock = func() time.Time { return c.IssuedAt.Add(time.Minute + time.Second) }
		}},
		{"attempt swap", func(_ *CompositeEffectSource, _ *offline.EffectChallengeV1, _ *offline.Binding, a *offline.Attempt) {
			a.ID = "other"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ss, cc, bb, aa, _ := compositeFixture(t)
			tc.mutate(&ss, &cc, &bb, &aa)
			if _, err := ss.ObserveEffect(context.Background(), cc, bb, aa); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}
