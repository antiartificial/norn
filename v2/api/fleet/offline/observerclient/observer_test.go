package observerclient

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

type stubClient struct {
	grant        offline.EffectGrant
	terminal     offline.TerminalEvidence
	terminalErr  error
	challengeErr error
	observes     int
	creates      int
}

func (s *stubClient) ReadConsumedGrant(context.Context, string, string) (offline.EffectGrant, error) {
	return s.grant, nil
}
func (s *stubClient) ReadTerminal(context.Context, string, string) (offline.TerminalEvidence, error) {
	return s.terminal, s.terminalErr
}
func (s *stubClient) ReadChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error) {
	return offline.TerminalChallengeV1{}, s.challengeErr
}
func (s *stubClient) CreateChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error) {
	s.creates++
	return offline.TerminalChallengeV1{}, ErrNotFound
}
func (s *stubClient) SubmitAttestation(context.Context, string, string, offline.SignedTerminalAttestationV1) (offline.TerminalEvidence, error) {
	t := s.terminal
	return t, nil
}

type stubSource struct{ calls int }

func (s *stubSource) Observe(context.Context, offline.TerminalChallengeV1, offline.EffectGrant) (offline.TerminalAttestationV1, error) {
	s.calls++
	return offline.TerminalAttestationV1{}, nil
}

func testRunner(client Client, source Source) Runner {
	_, key, _ := ed25519.GenerateKey(nil)
	return Runner{Client: client, Source: source, ObserverSubject: "observer", KeyID: "key-1", PrivateKey: key, Clock: time.Now}
}

func testGrant() offline.EffectGrant {
	now := time.Now().UTC()
	return offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema, ID: "grant", PlanID: "plan", AttemptID: "attempt", BindingSHA256: "binding", TargetID: "target", FenceGeneration: 2, AuthorityEpoch: 3, Phase: "consumed", ConsumedAt: &now,
		Backend: offline.Backend{Bucket: "bucket", Key: "key", Lineage: "lineage", VersionID: "old", Serial: 1}}
}

func receiptFor(g offline.EffectGrant, observed time.Time) offline.TerminalEvidence {
	backend := g.Backend
	backend.Serial++
	backend.VersionID = "new"
	backend.RawSHA256 = strings.Repeat("9", 64)
	return offline.TerminalEvidence{SchemaVersion: offline.TerminalSchema, PlanID: g.PlanID, AttemptID: g.AttemptID,
		GrantID: g.ID, BindingSHA256: g.BindingSHA256, TargetID: g.TargetID, FenceGeneration: g.FenceGeneration,
		AuthorityEpoch: g.AuthorityEpoch, ObservedAt: observed, RecordedAt: observed,
		EvidenceSHA256: strings.Repeat("a", 64), ProviderEvidenceSHA256: strings.Repeat("b", 64),
		BackendEvidenceSHA256: strings.Repeat("c", 64), WorkloadEvidenceSHA256: strings.Repeat("d", 64), Backend: backend}
}

func TestExistingExactTerminalReceiptSkipsObservation(t *testing.T) {
	g := testGrant()
	s := &stubSource{}
	c := &stubClient{grant: g, terminal: receiptFor(g, g.ConsumedAt.Add(time.Second))}
	if _, err := testRunner(c, s).Run(context.Background(), "plan", "attempt"); err != nil || s.calls != 0 || c.creates != 0 {
		t.Fatalf("exact receipt should recover without observation or challenge: err=%v source=%d create=%d", err, s.calls, c.creates)
	}
}

func TestAmbiguousTerminalReadFailsClosed(t *testing.T) {
	s := &stubSource{}
	c := &stubClient{grant: testGrant(), terminalErr: errors.New("timeout")}
	if _, err := testRunner(c, s).Run(context.Background(), "plan", "attempt"); err == nil || s.calls != 0 || c.creates != 0 {
		t.Fatalf("ambiguous terminal read must stop: err=%v source=%d create=%d", err, s.calls, c.creates)
	}
}

func TestMissingChallengeReadFailureDoesNotCreateOrObserve(t *testing.T) {
	s := &stubSource{}
	c := &stubClient{grant: testGrant(), terminalErr: ErrNotFound, challengeErr: errors.New("unavailable")}
	if _, err := testRunner(c, s).Run(context.Background(), "plan", "attempt"); err == nil || s.calls != 0 || c.creates != 0 {
		t.Fatalf("ambiguous challenge read must stop: err=%v source=%d create=%d", err, s.calls, c.creates)
	}
}

type flowClient struct {
	grant              offline.EffectGrant
	challenge          offline.TerminalChallengeV1
	terminal           offline.TerminalEvidence
	reads, submissions int
	responseLost       bool
}

func (s *flowClient) ReadConsumedGrant(context.Context, string, string) (offline.EffectGrant, error) {
	return s.grant, nil
}
func (s *flowClient) ReadTerminal(context.Context, string, string) (offline.TerminalEvidence, error) {
	s.reads++
	if s.reads == 1 {
		return offline.TerminalEvidence{}, ErrNotFound
	}
	return s.terminal, nil
}
func (s *flowClient) ReadChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error) {
	return s.challenge, nil
}
func (s *flowClient) CreateChallenge(context.Context, string, string) (offline.TerminalChallengeV1, error) {
	panic("existing challenge must be reused")
}
func (s *flowClient) SubmitAttestation(_ context.Context, _, _ string, envelope offline.SignedTerminalAttestationV1) (offline.TerminalEvidence, error) {
	s.submissions++
	s.terminal = receiptFor(s.grant, envelope.Attestation.ObservedTo)
	if s.responseLost {
		return offline.TerminalEvidence{}, errors.New("response lost after commit")
	}
	return s.terminal, nil
}

type flowSource struct{ calls int }

func (s *flowSource) Observe(_ context.Context, c offline.TerminalChallengeV1, g offline.EffectGrant) (offline.TerminalAttestationV1, error) {
	s.calls++
	observed := g.ConsumedAt.Add(3 * time.Second)
	backend := g.Backend
	backend.Serial++
	backend.VersionID = "new-version"
	backend.RawSHA256 = strings.Repeat("9", 64)
	o := offline.TerminalObservation{PlanID: g.PlanID, AttemptID: g.AttemptID, GrantID: g.ID,
		BindingSHA256: g.BindingSHA256, TargetID: g.TargetID, FenceGeneration: g.FenceGeneration, AuthorityEpoch: g.AuthorityEpoch,
		ObservedAt: observed, EvidenceSHA256: strings.Repeat("a", 64), ProviderEvidenceSHA256: strings.Repeat("b", 64),
		BackendEvidenceSHA256: strings.Repeat("c", 64), WorkloadEvidenceSHA256: strings.Repeat("d", 64),
		ProviderAccountUUID: g.ProviderAccountUUID, NodeName: g.NodeName, DropletID: g.DropletID, Backend: backend,
		ProviderNodeAbsent: true, BackendLatestExact: true, BackendNodeAbsent: true, AllNamespacesInspected: true,
		WorkloadNodeAbsent: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}
	return offline.NewTerminalAttestationV1(c, o, observed.Add(-2*time.Second), strings.Repeat("e", 64))
}

func TestLostSubmitResponseRecoversWithoutSecondEffect(t *testing.T) {
	now := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
	consumed := now.Add(-3 * time.Second)
	g := offline.EffectGrant{SchemaVersion: offline.EffectGrantSchema,
		ID: "33333333-3333-4333-8333-333333333333", PlanID: "11111111-1111-4111-8111-111111111111",
		AttemptID: "22222222-2222-4222-8222-222222222222", BindingSHA256: strings.Repeat("1", 64), TargetID: "target-1",
		FenceGeneration: 29, AuthorityEpoch: 2, SourceCommit: strings.Repeat("a", 40), SavedPlanSHA256: strings.Repeat("b", 64),
		EffectSHA256: strings.Repeat("c", 64), Backend: offline.Backend{Bucket: "bucket", Key: "key", VersionID: "old-version",
			Lineage: "lineage", Serial: 22, RawSHA256: strings.Repeat("d", 64)},
		ProviderAccountUUID: "work-account", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID,
		Phase: "consumed", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), ConsumedAt: &consumed}
	c, err := offline.NewTerminalChallengeV1(g, "verifier", "observer", strings.Repeat("f", 64),
		"44444444-4444-4444-8444-444444444444", make([]byte, 32), now.Add(-2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	client := &flowClient{grant: g, challenge: c, responseLost: true}
	source := &flowSource{}
	runner := testRunner(client, source)
	runner.Clock = func() time.Time { return now }
	got, err := runner.Run(context.Background(), g.PlanID, g.AttemptID)
	if err != nil || got.EvidenceSHA256 != strings.Repeat("a", 64) || client.submissions != 1 || client.reads != 2 || source.calls != 1 {
		t.Fatalf("ambiguous response recovery: receipt=%+v err=%v submissions=%d reads=%d observations=%d", got, err, client.submissions, client.reads, source.calls)
	}
}
