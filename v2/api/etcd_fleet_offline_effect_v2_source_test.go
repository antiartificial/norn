package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/fleet/offline"
)

type offlineEffectSourceState struct {
	binding offline.Binding
	attempt offline.Attempt
	grant   offline.EffectGrantV2
}

type offlineEffectTokenInfo struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (i offlineEffectTokenInfo) Sys() any { return i.stat }

func (s offlineEffectSourceState) GetOfflineDispatchReservation(context.Context, string) (offline.Binding, error) {
	return s.binding, nil
}
func (s offlineEffectSourceState) GetOfflineAttempt(context.Context, string) (offline.Attempt, error) {
	return s.attempt, nil
}
func (s offlineEffectSourceState) GetOfflineEffectGrantV2(context.Context, string) (offline.EffectGrantV2, error) {
	return s.grant, nil
}

func offlineEffectSourceFixture(t *testing.T) (offlineEffectSourceState, offlineEffectEvidenceV2) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	planID, attemptID, grantID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	names := []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"}
	nodes := make([]offline.ProviderNode, len(names))
	for i, name := range names {
		nodes[i] = offline.ProviderNode{Name: name, ProviderID: "70000000" + string(rune('1'+i))}
		if name == offline.WorkNode {
			nodes[i].ProviderID = offline.WorkDropletID
		}
	}
	binding := offline.Binding{SchemaVersion: offline.BindingSchemaV2, PlanID: planID, TargetID: "work-staging/app-03",
		Resource: offline.WorkResource, SourceCommit: strings.Repeat("a", 40), SavedPlanSHA256: strings.Repeat("b", 64),
		EffectSHA256: strings.Repeat("c", 64), CandidateSHA256: strings.Repeat("d", 64), ProviderNodes: nodes,
		Backend:             offline.Backend{Bucket: "state", Key: "work-staging.tfstate", VersionID: "version-42", Lineage: uuid.NewString(), Serial: 42, RawSHA256: strings.Repeat("e", 64)},
		ProviderAccountUUID: "work-account", NodeName: offline.WorkNode, DropletID: offline.WorkDropletID,
		OwnerAuthorizationID: strings.Repeat("1", 64), OwnerEnvelopeSHA256: strings.Repeat("2", 64),
		RunnerPrincipalSubject: "fleet-offline-device:" + uuid.NewString(), DispatchNonceSHA256: strings.Repeat("3", 64),
		FenceGeneration: 7, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(10 * time.Minute)}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	attempt := offline.Attempt{SchemaVersion: offline.AttemptSchema, ID: attemptID, PlanID: planID, BindingSHA256: strings.Repeat("4", 64),
		TargetID: binding.TargetID, FenceGeneration: binding.FenceGeneration, Status: "running", CurrentPhase: "prechange_verified", CreatedAt: now.Add(-time.Minute)}
	fence := offline.WriterFence{BackendID: "work-staging-postgres", Generation: 41, Holder: attemptID, SessionSHA256: strings.Repeat("5", 64)}
	terminal := &offline.TerminalEvidenceV2{SchemaVersion: offline.TerminalEvidenceSchemaV2, PlanID: planID, AttemptID: attemptID,
		GrantID: grantID, BindingSHA256: attempt.BindingSHA256, TargetID: binding.TargetID, TargetFenceGeneration: binding.FenceGeneration,
		WriterFence: fence, AuthorityEpoch: 9, ObservedAt: now.Add(-3 * time.Second), RecordedAt: now.Add(-2 * time.Second), EvidenceSHA256: strings.Repeat("6", 64)}
	grant := offline.EffectGrantV2{SchemaVersion: offline.EffectGrantSchemaV2, ID: grantID, PlanID: planID, AttemptID: attemptID,
		BindingSHA256: attempt.BindingSHA256, TargetID: binding.TargetID, TargetFenceGeneration: binding.FenceGeneration,
		AuthorityEpoch: 9, WriterFence: fence, Terminal: terminal, Phase: "terminal_verified"}
	evidence := offlineEffectEvidenceV2{SchemaVersion: offlineEffectEvidenceSchemaV2, PlanID: planID, AttemptID: attemptID,
		BindingSHA256: attempt.BindingSHA256, TargetID: binding.TargetID, TargetFenceGeneration: binding.FenceGeneration,
		AuthorityEpoch: 9, SourceCommit: binding.SourceCommit, SavedPlanSHA256: binding.SavedPlanSHA256, EffectSHA256: binding.EffectSHA256,
		Backend: binding.Backend, LatestBackend: binding.Backend, ProviderAccountUUID: binding.ProviderAccountUUID, NodeName: binding.NodeName, DropletID: binding.DropletID,
		WriterFence: fence, WriterState: "held", ProviderTargetState: "absent", BackendLatestState: "exact", BackendTargetState: "absent",
		WorkloadTargetState: "absent", NamespaceCoverage: "all", SurvivingPlacement: "passing", SurvivingHeadroom: "passing", ServiceState: "passing",
		ObservedAt: now, ValidUntil: now.Add(20 * time.Second), EvidenceSHA256: strings.Repeat("7", 64),
		ProviderEvidenceSHA256: strings.Repeat("8", 64), BackendEvidenceSHA256: strings.Repeat("9", 64), WorkloadEvidenceSHA256: strings.Repeat("a", 64),
		TerminalEvidenceSHA256: terminal.EvidenceSHA256, ReleaseReceiptSHA256: strings.Repeat("b", 64), ReleasedAt: now.Add(-time.Second)}
	return offlineEffectSourceState{binding: binding, attempt: attempt, grant: grant}, evidence
}

func TestOfflineEffectEvidenceSourceV2ReadsExactServerOwnedFacts(t *testing.T) {
	state, evidence := offlineEffectSourceFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Fatalf("request method/auth = %s/%q", r.Method, r.Header.Get("Authorization"))
		}
		row := evidence
		switch {
		case strings.HasSuffix(r.URL.Path, "/effect"):
			row.Phase, row.GrantID = "effect", ""
		case strings.HasSuffix(r.URL.Path, "/terminal"):
			row.Phase, row.GrantID, row.BackendLatestState = "terminal", state.grant.ID, "advanced"
			row.LatestBackend.Serial, row.LatestBackend.VersionID, row.LatestBackend.RawSHA256 = row.Backend.Serial+1, "version-43", strings.Repeat("f", 64)
		case strings.HasSuffix(r.URL.Path, "/completion"):
			row.Phase, row.GrantID, row.WriterState, row.BackendLatestState = "completion", state.grant.ID, "released", "advanced"
			row.LatestBackend.Serial, row.LatestBackend.VersionID, row.LatestBackend.RawSHA256 = row.Backend.Serial+1, "version-43", strings.Repeat("f", 64)
		default:
			t.Fatalf("unexpected verifier path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(row)
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "effect-token")
	if err := os.WriteFile(tokenFile, []byte("scoped-token"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := newOfflineEffectEvidenceSourceV2(&config.Config{FleetOfflineEffectV2Enabled: true,
		FleetOfflineEffectV2VerifierURL: server.URL, FleetOfflineEffectV2TokenFile: tokenFile}, state)
	if err != nil {
		t.Fatal(err)
	}
	if observed, err := source.ObserveOfflineEffect(context.Background(), state.binding.PlanID, state.attempt.ID); err != nil ||
		observed.WriterFence != evidence.WriterFence || !observed.ProviderExact || !observed.TargetEmpty {
		t.Fatalf("effect observation=%+v err=%v", observed, err)
	}
	if observed, err := source.ObserveOfflineTerminal(context.Background(), state.binding.PlanID, state.attempt.ID, state.grant.ID); err != nil ||
		!observed.ProviderAbsent || !observed.BackendAdvanced || !observed.WorkloadTargetAbsent {
		t.Fatalf("terminal observation=%+v err=%v", observed, err)
	}
	if observed, err := source.ObserveOfflineCompletion(context.Background(), state.binding.PlanID, state.attempt.ID, state.grant.ID); err != nil ||
		observed.ReceiptSHA256 != evidence.ReleaseReceiptSHA256 || observed.TerminalEvidenceSHA256 != state.grant.Terminal.EvidenceSHA256 {
		t.Fatalf("completion observation=%+v err=%v", observed, err)
	}
}

func TestOfflineEffectEvidenceSourceV2FailsClosed(t *testing.T) {
	state, evidence := offlineEffectSourceFixture(t)
	evidence.DropletID = "610327216"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		evidence.Phase = "effect"
		_ = json.NewEncoder(w).Encode(evidence)
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "effect-token")
	if err := os.WriteFile(tokenFile, []byte("scoped-token"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := newOfflineEffectEvidenceSourceV2(&config.Config{FleetOfflineEffectV2Enabled: true,
		FleetOfflineEffectV2VerifierURL: server.URL, FleetOfflineEffectV2TokenFile: tokenFile}, state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ObserveOfflineEffect(context.Background(), state.binding.PlanID, state.attempt.ID); err == nil {
		t.Fatal("mismatched provider target identity was accepted")
	}
	if source, err := newOfflineEffectEvidenceSourceV2(&config.Config{}, state); err != nil || source != nil {
		t.Fatalf("default-off source=%T err=%v", source, err)
	}
	if err := os.Chmod(tokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newOfflineEffectEvidenceSourceV2(&config.Config{FleetOfflineEffectV2Enabled: true,
		FleetOfflineEffectV2VerifierURL: server.URL, FleetOfflineEffectV2TokenFile: tokenFile}, state); err == nil {
		t.Fatal("broadly readable evidence credential was accepted")
	}

	privateToken := filepath.Join(t.TempDir(), "private-token")
	if err := os.WriteFile(privateToken, []byte("scoped-token"), 0600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "token-link")
	if err := os.Symlink(privateToken, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readOfflineEffectToken(symlink); err == nil {
		t.Fatal("symlinked evidence credential was accepted")
	}

	largeToken := filepath.Join(t.TempDir(), "large-token")
	if err := os.WriteFile(largeToken, []byte(strings.Repeat("x", offlineEffectTokenMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOfflineEffectToken(largeToken); err == nil {
		t.Fatal("oversized evidence credential was accepted")
	}

	info, err := os.Stat(privateToken)
	if err != nil {
		t.Fatal(err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid++
	if err := validateOfflineEffectTokenInfo(offlineEffectTokenInfo{FileInfo: info, stat: &stat}, uint32(os.Geteuid())); err == nil {
		t.Fatal("evidence credential owned by another user was accepted")
	}
}

func TestOfflineEffectEvidenceV2CapabilityIsConditional(t *testing.T) {
	disabled := etcdFleetCapabilities(&config.Config{}, false)
	if containsString(disabled["features"].([]string), "fleet-offline-effect-evidence-v2") ||
		disabled["endpoints"].(map[string]string)["fleetOfflineEffectV2"] != "" {
		t.Fatal("disabled offline effect v2 source was advertised")
	}
	enabled := etcdFleetCapabilities(&config.Config{FleetOfflineEffectV2Enabled: true}, false)
	if !containsString(enabled["features"].([]string), "fleet-offline-effect-evidence-v2") ||
		enabled["endpoints"].(map[string]string)["fleetOfflineEffectV2"] == "" {
		t.Fatal("enabled offline effect v2 source was not advertised")
	}
}
