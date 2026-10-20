package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/config"
	"norn/v2/api/fleet/offline"
)

const offlineEffectEvidenceSchemaV2 = "norn.fleet-offline-independent-effect-evidence/v2"

const offlineEffectTokenMaxBytes = 4096

var offlineEffectHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// offlineEffectEvidenceState is the server-owned side of the observation.
// The runner supplies none of these records or evidence facts.
type offlineEffectEvidenceState interface {
	GetOfflineDispatchReservation(context.Context, string) (offline.Binding, error)
	GetOfflineAttempt(context.Context, string) (offline.Attempt, error)
	GetOfflineEffectGrantV2(context.Context, string) (offline.EffectGrantV2, error)
}

// offlineEffectEvidenceV2 is the exact response from a separately
// credentialed, read-only verifier. State strings are used instead of a
// generic success boolean so Norn can require each independently observed
// condition. The verifier service, rather than the offline runner, owns these
// observations.
type offlineEffectEvidenceV2 struct {
	SchemaVersion          string              `json:"schemaVersion"`
	Phase                  string              `json:"phase"`
	PlanID                 string              `json:"planId"`
	AttemptID              string              `json:"attemptId"`
	GrantID                string              `json:"grantId,omitempty"`
	BindingSHA256          string              `json:"bindingSHA256"`
	TargetID               string              `json:"targetId"`
	TargetFenceGeneration  uint64              `json:"targetFenceGeneration"`
	AuthorityEpoch         int64               `json:"authorityEpoch"`
	SourceCommit           string              `json:"sourceCommit"`
	SavedPlanSHA256        string              `json:"savedPlanSHA256"`
	EffectSHA256           string              `json:"effectSHA256"`
	Backend                offline.Backend     `json:"backend"`
	LatestBackend          offline.Backend     `json:"latestBackend"`
	ProviderAccountUUID    string              `json:"providerAccountUUID"`
	NodeName               string              `json:"nodeName"`
	DropletID              string              `json:"dropletId"`
	WriterFence            offline.WriterFence `json:"writerFence"`
	WriterState            string              `json:"writerState"`
	ProviderTargetState    string              `json:"providerTargetState"`
	BackendLatestState     string              `json:"backendLatestState"`
	BackendTargetState     string              `json:"backendTargetState"`
	WorkloadTargetState    string              `json:"workloadTargetState"`
	NamespaceCoverage      string              `json:"namespaceCoverage"`
	SurvivingPlacement     string              `json:"survivingPlacement"`
	SurvivingHeadroom      string              `json:"survivingHeadroom"`
	ServiceState           string              `json:"serviceState"`
	ObservedAt             time.Time           `json:"observedAt"`
	ValidUntil             time.Time           `json:"validUntil,omitempty"`
	ReleasedAt             time.Time           `json:"releasedAt,omitempty"`
	EvidenceSHA256         string              `json:"evidenceSHA256"`
	ProviderEvidenceSHA256 string              `json:"providerEvidenceSHA256,omitempty"`
	BackendEvidenceSHA256  string              `json:"backendEvidenceSHA256,omitempty"`
	WorkloadEvidenceSHA256 string              `json:"workloadEvidenceSHA256,omitempty"`
	TerminalEvidenceSHA256 string              `json:"terminalEvidenceSHA256,omitempty"`
	ReleaseReceiptSHA256   string              `json:"releaseReceiptSHA256,omitempty"`
}

type httpOfflineEffectEvidenceSourceV2 struct {
	baseURL *url.URL
	token   string
	client  *http.Client
	state   offlineEffectEvidenceState
}

func newOfflineEffectEvidenceSourceV2(cfg *config.Config, state offlineEffectEvidenceState) (OfflineEffectEvidenceSourceV2, error) {
	if cfg == nil || !cfg.FleetOfflineEffectV2Enabled {
		return nil, nil
	}
	if state == nil || cfg.FleetOfflineEffectV2VerifierURL == "" || cfg.FleetOfflineEffectV2TokenFile == "" {
		return nil, errors.New("offline effect v2 requires an evidence state store, verifier URL, and dedicated token file")
	}
	parsed, err := url.Parse(cfg.FleetOfflineEffectV2VerifierURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
		return nil, errors.New("offline effect v2 verifier requires HTTPS or loopback HTTP")
	}
	raw, err := readOfflineEffectToken(cfg.FleetOfflineEffectV2TokenFile)
	token := strings.TrimSpace(string(raw))
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("offline effect v2 token file is unreadable or invalid")
	}
	return &httpOfflineEffectEvidenceSourceV2{baseURL: parsed, token: token, state: state, client: &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("offline effect v2 redirects are forbidden")
		},
	}}, nil
}

func readOfflineEffectToken(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("offline effect v2 token file must be a regular owner-only file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || validateOfflineEffectTokenInfo(info, uint32(os.Geteuid())) != nil {
		return nil, errors.New("offline effect v2 token file must be a regular owner-only file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, offlineEffectTokenMaxBytes+1))
	if err != nil || len(raw) > offlineEffectTokenMaxBytes {
		return nil, errors.New("offline effect v2 token file is unreadable or too large")
	}
	return raw, nil
}

func validateOfflineEffectTokenInfo(info os.FileInfo, euid uint32) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("not an owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != euid || stat.Nlink != 1 {
		return errors.New("token file has wrong owner or link count")
	}
	return nil
}

func (s *httpOfflineEffectEvidenceSourceV2) ObserveOfflineEffect(ctx context.Context, planID, attemptID string) (offline.EffectObservationV2, error) {
	binding, attempt, err := s.boundState(ctx, planID, attemptID)
	if err != nil {
		return offline.EffectObservationV2{}, err
	}
	e, err := s.observe(ctx, "effect", planID, attemptID, "")
	if err != nil || !s.matchesBinding(e, binding, attempt, "effect", "") || e.WriterState != "held" ||
		e.ProviderTargetState != "present" || e.BackendLatestState != "exact" || e.LatestBackend != binding.Backend || e.BackendTargetState != "present" ||
		e.WorkloadTargetState != "absent" || e.NamespaceCoverage != "all" || e.SurvivingPlacement != "passing" ||
		e.SurvivingHeadroom != "passing" || e.ServiceState != "passing" || e.ValidUntil.IsZero() || !offlineEffectHex64.MatchString(e.EvidenceSHA256) {
		return offline.EffectObservationV2{}, errors.New("independent effect evidence differs from server state")
	}
	return offline.EffectObservationV2{PlanID: e.PlanID, AttemptID: e.AttemptID, BindingSHA256: e.BindingSHA256,
		TargetID: e.TargetID, SourceCommit: e.SourceCommit, SavedPlanSHA256: e.SavedPlanSHA256, EffectSHA256: e.EffectSHA256,
		Backend: e.Backend, ProviderAccountUUID: e.ProviderAccountUUID, NodeName: e.NodeName, DropletID: e.DropletID,
		FenceGeneration: e.TargetFenceGeneration, AuthorityEpoch: e.AuthorityEpoch, WriterFence: e.WriterFence,
		ObservedAt: e.ObservedAt, ValidUntil: e.ValidUntil, EvidenceSHA256: e.EvidenceSHA256,
		ProviderExact: true, LatestBackendExact: true, AllNamespacesInspected: true,
		ProviderTargetPresent: true, BackendTargetPresent: true,
		SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) ObserveOfflineTerminal(ctx context.Context, planID, attemptID, grantID string) (offline.TerminalObservationV2, error) {
	binding, attempt, grant, err := s.grantState(ctx, planID, attemptID, grantID)
	if err != nil {
		return offline.TerminalObservationV2{}, err
	}
	e, err := s.observe(ctx, "terminal", planID, attemptID, grantID)
	if err != nil || !s.matchesBinding(e, binding, attempt, "terminal", grantID) || !matchesGrant(e, grant) ||
		e.WriterState != "held" || e.ProviderTargetState != "absent" || e.BackendLatestState != "advanced" || !advancedBackend(binding.Backend, e.LatestBackend) ||
		e.BackendTargetState != "absent" || e.WorkloadTargetState != "absent" || e.NamespaceCoverage != "all" ||
		e.SurvivingPlacement != "passing" || e.SurvivingHeadroom != "passing" || e.ServiceState != "passing" ||
		!digests(e.EvidenceSHA256, e.ProviderEvidenceSHA256, e.BackendEvidenceSHA256, e.WorkloadEvidenceSHA256) {
		return offline.TerminalObservationV2{}, errors.New("independent terminal evidence differs from server state")
	}
	return offline.TerminalObservationV2{PlanID: e.PlanID, AttemptID: e.AttemptID, GrantID: e.GrantID,
		BindingSHA256: e.BindingSHA256, TargetID: e.TargetID, TargetFenceGeneration: e.TargetFenceGeneration,
		AuthorityEpoch: e.AuthorityEpoch, WriterFence: e.WriterFence, ObservedAt: e.ObservedAt,
		EvidenceSHA256: e.EvidenceSHA256, ProviderEvidenceSHA256: e.ProviderEvidenceSHA256,
		BackendEvidenceSHA256: e.BackendEvidenceSHA256, WorkloadEvidenceSHA256: e.WorkloadEvidenceSHA256,
		ProviderAbsent: true, BackendAdvanced: true, BackendTargetAbsent: true, AllNamespacesInspected: true,
		WorkloadTargetAbsent: true, SurvivingPlacement: true, SurvivingHeadroom: true, ServicePassing: true}, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) ObserveOfflineCompletion(ctx context.Context, planID, attemptID, grantID string) (offline.CompletionObservationV2, error) {
	binding, attempt, grant, err := s.grantState(ctx, planID, attemptID, grantID)
	if err != nil {
		return offline.CompletionObservationV2{}, err
	}
	e, err := s.observe(ctx, "completion", planID, attemptID, grantID)
	if err != nil || !s.matchesBinding(e, binding, attempt, "completion", grantID) || !matchesGrant(e, grant) || grant.Terminal == nil ||
		e.WriterState != "released" || e.TerminalEvidenceSHA256 != grant.Terminal.EvidenceSHA256 ||
		e.ProviderTargetState != "absent" || e.BackendLatestState != "advanced" || !advancedBackend(binding.Backend, e.LatestBackend) ||
		e.BackendTargetState != "absent" || e.WorkloadTargetState != "absent" || e.NamespaceCoverage != "all" ||
		e.SurvivingPlacement != "passing" || e.SurvivingHeadroom != "passing" || e.ServiceState != "passing" ||
		!digests(e.ProviderEvidenceSHA256, e.BackendEvidenceSHA256, e.WorkloadEvidenceSHA256, e.ReleaseReceiptSHA256) ||
		e.ReleasedAt.IsZero() || e.ReleasedAt.Before(grant.Terminal.RecordedAt) {
		return offline.CompletionObservationV2{}, errors.New("independent completion evidence differs from server state")
	}
	return offline.CompletionObservationV2{PlanID: e.PlanID, AttemptID: e.AttemptID, GrantID: e.GrantID,
		BindingSHA256: e.BindingSHA256, TargetID: e.TargetID, TargetFenceGeneration: e.TargetFenceGeneration,
		AuthorityEpoch: e.AuthorityEpoch, WriterFence: e.WriterFence, TerminalEvidenceSHA256: e.TerminalEvidenceSHA256,
		ReceiptSHA256: e.ReleaseReceiptSHA256, ReleasedAt: e.ReleasedAt, ObservedAt: e.ObservedAt}, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) boundState(ctx context.Context, planID, attemptID string) (offline.Binding, offline.Attempt, error) {
	if s == nil || s.state == nil {
		return offline.Binding{}, offline.Attempt{}, errors.New("offline effect evidence source unavailable")
	}
	binding, err := s.state.GetOfflineDispatchReservation(ctx, planID)
	if err != nil {
		return offline.Binding{}, offline.Attempt{}, err
	}
	attempt, err := s.state.GetOfflineAttempt(ctx, planID)
	if err != nil {
		return offline.Binding{}, offline.Attempt{}, err
	}
	if binding.SchemaVersion != offline.BindingSchemaV2 || binding.Validate() != nil || binding.NodeName != offline.WorkNode ||
		attempt.ID != attemptID || attempt.PlanID != planID || !offlineEffectHex64.MatchString(attempt.BindingSHA256) ||
		attempt.TargetID != binding.TargetID || attempt.FenceGeneration != binding.FenceGeneration {
		return offline.Binding{}, offline.Attempt{}, errors.New("offline effect server state is not the exact app-03 v2 binding")
	}
	return binding, attempt, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) grantState(ctx context.Context, planID, attemptID, grantID string) (offline.Binding, offline.Attempt, offline.EffectGrantV2, error) {
	binding, attempt, err := s.boundState(ctx, planID, attemptID)
	if err != nil {
		return offline.Binding{}, offline.Attempt{}, offline.EffectGrantV2{}, err
	}
	grant, err := s.state.GetOfflineEffectGrantV2(ctx, planID)
	if err != nil || grant.ID != grantID || grant.AttemptID != attemptID || grant.BindingSHA256 != attempt.BindingSHA256 {
		return offline.Binding{}, offline.Attempt{}, offline.EffectGrantV2{}, errors.New("offline effect grant differs from server state")
	}
	return binding, attempt, grant, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) observe(ctx context.Context, phase, planID, attemptID, grantID string) (offlineEffectEvidenceV2, error) {
	if s == nil || s.baseURL == nil || s.client == nil || s.token == "" {
		return offlineEffectEvidenceV2{}, errors.New("offline effect evidence source unavailable")
	}
	endpoint := *s.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v2/offline-effects/" + url.PathEscape(planID) + "/" + url.PathEscape(attemptID)
	if grantID != "" {
		endpoint.Path += "/" + url.PathEscape(grantID)
	}
	endpoint.Path += "/" + phase
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return offlineEffectEvidenceV2{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return offlineEffectEvidenceV2{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return offlineEffectEvidenceV2{}, fmt.Errorf("offline effect verifier status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024+1))
	if err != nil || len(raw) > 32*1024 || offlineReservationUniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return offlineEffectEvidenceV2{}, errors.New("invalid offline effect evidence response")
	}
	var evidence offlineEffectEvidenceV2
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&evidence) != nil || decoder.Decode(&struct{}{}) != io.EOF || evidence.SchemaVersion != offlineEffectEvidenceSchemaV2 {
		return offlineEffectEvidenceV2{}, errors.New("invalid offline effect evidence response")
	}
	return evidence, nil
}

func (s *httpOfflineEffectEvidenceSourceV2) matchesBinding(e offlineEffectEvidenceV2, b offline.Binding, a offline.Attempt, phase, grantID string) bool {
	return e.Phase == phase && e.PlanID == b.PlanID && e.AttemptID == a.ID && e.GrantID == grantID &&
		e.BindingSHA256 == a.BindingSHA256 && e.TargetID == b.TargetID && e.TargetFenceGeneration == b.FenceGeneration &&
		e.AuthorityEpoch > 0 && e.SourceCommit == b.SourceCommit && e.SavedPlanSHA256 == b.SavedPlanSHA256 &&
		e.EffectSHA256 == b.EffectSHA256 && e.Backend == b.Backend && e.ProviderAccountUUID == b.ProviderAccountUUID &&
		e.NodeName == offline.WorkNode && e.DropletID == offline.WorkDropletID && e.WriterFence.Holder == a.ID &&
		e.WriterFence.BackendID != "" && e.WriterFence.Generation > 0 && offlineEffectHex64.MatchString(e.WriterFence.SessionSHA256) &&
		!e.ObservedAt.IsZero()
}

func matchesGrant(e offlineEffectEvidenceV2, g offline.EffectGrantV2) bool {
	return e.GrantID == g.ID && e.AuthorityEpoch == g.AuthorityEpoch && e.WriterFence == g.WriterFence &&
		e.TargetFenceGeneration == g.TargetFenceGeneration
}

func digests(values ...string) bool {
	for _, value := range values {
		if !offlineEffectHex64.MatchString(value) {
			return false
		}
	}
	return true
}

func advancedBackend(before, after offline.Backend) bool {
	return after.Bucket == before.Bucket && after.Key == before.Key && after.Lineage == before.Lineage &&
		after.Serial > before.Serial && after.VersionID != "" && after.VersionID != before.VersionID &&
		offlineEffectHex64.MatchString(after.RawSHA256) && after.RawSHA256 != before.RawSHA256
}

var _ OfflineEffectEvidenceSourceV2 = (*httpOfflineEffectEvidenceSourceV2)(nil)
