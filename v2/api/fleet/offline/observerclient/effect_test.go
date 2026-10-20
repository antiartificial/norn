package observerclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type effectClientStub struct {
	challenge    offline.EffectChallengeV1
	accepted     AcceptedEffectAttestation
	puts         int
	lostResponse bool
	acceptedAt   *time.Time
}

func (s *effectClientStub) ReadEffectChallenge(context.Context, string, string, string) (offline.EffectChallengeV1, error) {
	return s.challenge, nil
}
func (s *effectClientStub) ReadAcceptedEffectAttestation(context.Context, string, string, string) (AcceptedEffectAttestation, error) {
	if s.accepted.Signed.KeyID == "" {
		return AcceptedEffectAttestation{}, ErrNotFound
	}
	return s.accepted, nil
}
func (s *effectClientStub) SubmitEffectAttestation(_ context.Context, _, _ string, signed offline.SignedEffectAttestationV1) error {
	s.puts++
	s.accepted = AcceptedEffectAttestation{Signed: signed, AcceptedAt: *s.acceptedAt}
	if s.lostResponse {
		return errors.New("lost response")
	}
	return nil
}

type effectSourceStub struct {
	calls int
	now   *time.Time
	b     offline.Binding
	a     offline.Attempt
}

func (s *effectSourceStub) ObserveEffect(_ context.Context, _ offline.EffectChallengeV1, _ offline.Binding, _ offline.Attempt) (EffectReads, error) {
	s.calls++
	*s.now = s.now.Add(2 * time.Second)
	b, a := s.b, s.a
	return EffectReads{Source: []byte("source transcript"), Provider: []byte("provider transcript"), Backend: []byte("backend transcript"), Workload: []byte("workload transcript"),
		Observation: offline.EffectObservation{PlanID: b.PlanID, AttemptID: a.ID, BindingSHA256: a.BindingSHA256, TargetID: b.TargetID,
			SourceCommit: b.SourceCommit, SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend,
			ProviderAccountUUID: b.ProviderAccountUUID, NodeName: b.NodeName, DropletID: b.DropletID, FenceGeneration: b.FenceGeneration,
			AuthorityEpoch: 2, ObservedAt: *s.now, EvidenceSHA256: strings.Repeat("a", 64), TrustedOrigin: true, ProviderExact: true,
			LatestBackendExact: true, AllNamespacesInspected: true, App03Empty: true, SurvivingPlacement: true,
			SurvivingHeadroom: true, ServicePassing: true}}, nil
}

func TestEffectRunnerIssueConsumeLostResponseAndExactReadback(t *testing.T) {
	for _, purpose := range []string{offline.EffectIssuePurpose, offline.EffectConsumePurpose} {
		t.Run(purpose, func(t *testing.T) {
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
			instant := start.Add(time.Minute)
			a := offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: "22222222-2222-4222-8222-222222222222", PlanID: b.PlanID,
				BindingSHA256: digestEffectBytes(bindingRaw), PrincipalSubject: b.RunnerPrincipalSubject, PrincipalTokenID: "token", PrincipalDeviceID: "device",
				TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, Status: "queued", CurrentPhase: "prechange_verified", CreatedAt: start.Add(10 * time.Second)}
			var grant *offline.EffectGrant
			if purpose == offline.EffectConsumePurpose {
				grant = &offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: "33333333-3333-4333-8333-333333333333", PlanID: b.PlanID, AttemptID: a.ID,
					BindingSHA256: a.BindingSHA256, TargetID: b.TargetID, FenceGeneration: b.FenceGeneration, AuthorityEpoch: 2, SourceCommit: b.SourceCommit,
					SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256, Backend: b.Backend, ProviderAccountUUID: b.ProviderAccountUUID,
					NodeName: b.NodeName, DropletID: b.DropletID, PrechangeSHA256: strings.Repeat("2", 64), IndependentlyObservedSHA256: strings.Repeat("3", 64),
					Phase: "issued", IssuedAt: instant.Add(-time.Second), ExpiresAt: instant.Add(59 * time.Second)}
			}
			public, private, _ := ed25519.GenerateKey(nil)
			challenge, err := offline.NewEffectChallengeV1(purpose, b, a, 2, "observer", "observer-key", 7, public, bytes.Repeat([]byte{1}, 32), grant, instant)
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(t.TempDir(), "journal")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			client := &effectClientStub{challenge: challenge, lostResponse: true, acceptedAt: &instant}
			source := &effectSourceStub{now: &instant, b: b, a: a}
			runner := EffectRunner{Client: client, Source: source, Journal: FileEffectJournal{Directory: directory}, ObserverSubject: "observer", KeyID: "observer-key",
				TrustGeneration: 7, PrivateKey: private, Clock: func() time.Time { return instant }}
			first, err := runner.RunEffect(context.Background(), b, a, 2, grant, purpose)
			if err != nil || first.KeyID == "" || client.puts != 1 || source.calls != 1 {
				t.Fatalf("response loss: %v puts=%d reads=%d", err, client.puts, source.calls)
			}
			second, err := runner.RunEffect(context.Background(), b, a, 2, grant, purpose)
			if err != nil || second.Signature != first.Signature || client.puts != 1 || source.calls != 1 {
				t.Fatalf("exact recovery: %v puts=%d reads=%d", err, client.puts, source.calls)
			}
			committed := client.accepted
			client.accepted = AcceptedEffectAttestation{}
			if _, err := runner.RunEffect(context.Background(), b, a, 2, grant, purpose); err == nil || client.puts != 1 || source.calls != 1 {
				t.Fatal("journaled submission without target receipt must wait without retry")
			}
			client.accepted = committed
			instant = challenge.ExpiresAt.Add(time.Second)
			if _, err := runner.RunEffect(context.Background(), b, a, 2, grant, purpose); err != nil {
				t.Fatalf("historical accepted readback: %v", err)
			}
			client.accepted.Signed.Attestation.ProviderEvidenceSHA256 = strings.Repeat("0", 64)
			if _, err := runner.RunEffect(context.Background(), b, a, 2, grant, purpose); err == nil {
				t.Fatal("accepted tampered readback")
			}
		})
	}
}
