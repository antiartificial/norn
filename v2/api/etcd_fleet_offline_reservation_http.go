package main

// Owner-signed admission for an offline Fleet reservation. The owner
// signature proves intent; a separately credentialed verifier proves the
// source, saved plan, backend object and node/provider identity before etcd is
// allowed to acquire the durable target fence.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"norn/v2/api/config"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
	"norn/v2/api/handler"
	"norn/v2/api/store"
)

type offlineReservationStore interface {
	ReserveOfflineDispatch(context.Context, offline.Binding, offline.VerifiedApproval, time.Time) (offline.Binding, error)
	GetOfflineDispatchReservation(context.Context, string) (offline.Binding, error)
}

// OfflineReservationEvidenceVerifier is deliberately independent of the
// caller and owner signature. Implementations must obtain evidence through a
// server-held credential and compare every destructive identity in Binding.
type OfflineReservationEvidenceVerifier interface {
	VerifyOfflineReservation(context.Context, offline.Binding) error
}

type offlineReservationRequest struct {
	Binding  json.RawMessage `json:"binding"`
	Approval json.RawMessage `json:"approval"`
}

func decodeOfflineReservationRequest(raw []byte) (offline.Binding, offline.SignedApproval, error) {
	var request offlineReservationRequest
	if err := offlineReservationUniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return offline.Binding{}, offline.SignedApproval{}, offline.ErrInvalidBinding
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 32*1024 || decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(request.Binding) == 0 || len(request.Approval) == 0 {
		return offline.Binding{}, offline.SignedApproval{}, offline.ErrInvalidBinding
	}
	binding, err := offline.DecodeBinding(request.Binding)
	if err != nil {
		return offline.Binding{}, offline.SignedApproval{}, err
	}
	approval, err := offline.DecodeSignedApproval(request.Approval)
	if err != nil {
		return offline.Binding{}, offline.SignedApproval{}, err
	}
	return binding, approval, nil
}

func offlineReservationUniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON field")
			}
			seen[name] = true
			if err := offlineReservationUniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := offlineReservationUniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func decodeOfflineOwnerPublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("offline owner public key must be one base64 Ed25519 public key")
	}
	return ed25519.PublicKey(raw), nil
}

func etcdOfflineReservationCreate(store offlineReservationStore, devices offlineAttemptDeviceStore, verifier OfflineReservationEvidenceVerifier,
	ownerKeyID string, ownerPublicKey ed25519.PublicKey) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := chi.URLParam(r, "planID")
		if uuid.Validate(planID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
			return
		}
		if store == nil || verifier == nil || ownerKeyID == "" || len(ownerPublicKey) != ed25519.PublicKeySize {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_reservation_unavailable", "offline reservation evidence verification is unavailable")
			return
		}
		raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 32*1024))
		binding, signed, decodeErr := decodeOfflineReservationRequest(raw)
		if readErr != nil || decodeErr != nil || binding.PlanID != planID {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_offline_reservation", "binding and owner approval must be strict and match the path")
			return
		}
		deviceID := strings.TrimPrefix(binding.RunnerPrincipalSubject, "fleet-offline-device:")
		if devices == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_reservation_unavailable", "offline device authority is unavailable")
			return
		}
		device, deviceErr := devices.ActiveAccessDevice(r.Context(), deviceID)
		now := time.Now().UTC()
		if deviceErr != nil || device == nil || uuid.Validate(deviceID) != nil ||
			binding.RunnerPrincipalSubject != "fleet-offline-device:"+deviceID || device.ID != deviceID || device.RevokedAt != nil ||
			!strings.EqualFold(strings.TrimSpace(device.Platform), handler.FleetOfflineRunnerPlatform) ||
			!offlineReservationDeviceTokenLive(device, binding.RunnerPrincipalSubject, now) {
			handler.WriteControlProblem(w, r, http.StatusPreconditionFailed, "offline_runner_not_enrolled", "runner principal must name a live enrolled offline device")
			return
		}
		approval, err := offline.VerifySignedApproval(signed, ownerKeyID, ownerPublicKey, binding, now)
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusForbidden, "invalid_offline_owner_approval", "owner approval is invalid or does not match the binding")
			return
		}
		if err := verifier.VerifyOfflineReservation(r.Context(), binding); err != nil {
			handler.WriteControlProblem(w, r, http.StatusPreconditionFailed, "offline_evidence_not_verified", "independent reservation evidence did not match")
			return
		}
		reserved, err := store.ReserveOfflineDispatch(r.Context(), binding, approval, now)
		if err != nil {
			status, code := http.StatusConflict, "offline_reservation_conflict"
			if !errors.Is(err, offline.ErrInvalidBinding) && !errors.Is(err, etcdstore.ErrOfflineDispatchReservationConflict) {
				status, code = http.StatusServiceUnavailable, "offline_reservation_uncertain"
			}
			handler.WriteControlProblem(w, r, status, code, "offline reservation was not committed; read exact state before retry")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(reserved)
	}
}

func offlineReservationDeviceTokenLive(device *store.AccessDevice, subject string, now time.Time) bool {
	for _, token := range device.Tokens {
		if token.DeviceID == device.ID && token.Subject == subject && token.RevokedAt == nil &&
			token.ExpiresAt.After(now) && len(token.Scopes) == 1 && token.Scopes[0] == handler.ScopeFleetOfflineAttempt {
			return true
		}
	}
	return false
}

func etcdOfflineReservationGet(store offlineReservationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		planID := chi.URLParam(r, "planID")
		if uuid.Validate(planID) != nil {
			handler.WriteControlProblem(w, r, http.StatusBadRequest, "invalid_fleet_plan_id", "plan ID must be a UUID")
			return
		}
		if store == nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_reservation_store_unavailable", "offline reservation store is unavailable")
			return
		}
		binding, err := store.GetOfflineDispatchReservation(r.Context(), planID)
		if errors.Is(err, etcdstore.ErrNotFound) {
			handler.WriteControlProblem(w, r, http.StatusNotFound, "offline_reservation_not_found", "offline reservation not found")
			return
		}
		if err != nil {
			handler.WriteControlProblem(w, r, http.StatusServiceUnavailable, "offline_reservation_read_failed", "offline reservation could not be read")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(w).Encode(binding)
	}
}

// offlineEvidence is the verifier service's authenticated response. It has no
// "verified" boolean: successful admission requires exact evidence values.
type offlineEvidence struct {
	SchemaVersion       string          `json:"schemaVersion"`
	PlanID              string          `json:"planId"`
	TargetID            string          `json:"targetId"`
	Resource            string          `json:"resource"`
	SourceCommit        string          `json:"sourceCommit"`
	SavedPlanSHA256     string          `json:"savedPlanSHA256"`
	EffectSHA256        string          `json:"effectSHA256"`
	Backend             offline.Backend `json:"backend"`
	ProviderAccountUUID string          `json:"providerAccountUUID"`
	NodeName            string          `json:"nodeName"`
	DropletID           string          `json:"dropletId"`
	ObservedAt          time.Time       `json:"observedAt"`
}

// httpOfflineEvidenceVerifier is a provisional protocol adapter. The Norn
// repository does not contain the upstream service that independently reads
// GitHub, versioned object storage and provider identity. Operators must not
// enable the route until that service is implemented and qualified.
type httpOfflineEvidenceVerifier struct {
	baseURL *url.URL
	token   string
	client  *http.Client
	now     func() time.Time
}

func newOfflineReservationRuntime(cfg *config.Config) (ed25519.PublicKey, OfflineReservationEvidenceVerifier, error) {
	if cfg == nil || !cfg.FleetOfflineReservationEnabled {
		return nil, nil, nil
	}
	if cfg.FleetOfflineOwnerKeyID == "" || cfg.FleetOfflineOwnerPublicKeyFile == "" ||
		cfg.FleetOfflineEvidenceVerifierURL == "" || cfg.FleetOfflineEvidenceTokenFile == "" {
		return nil, nil, errors.New("offline reservation requires owner key ID, owner public key, evidence verifier URL, and evidence token file")
	}
	keyBytes, err := os.ReadFile(cfg.FleetOfflineOwnerPublicKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read offline owner public key: %w", err)
	}
	key, err := decodeOfflineOwnerPublicKey(string(keyBytes))
	if err != nil {
		return nil, nil, err
	}
	verifier, err := newHTTPOfflineEvidenceVerifier(cfg.FleetOfflineEvidenceVerifierURL, cfg.FleetOfflineEvidenceTokenFile)
	if err != nil {
		return nil, nil, err
	}
	return key, verifier, nil
}

func newHTTPOfflineEvidenceVerifier(rawURL, tokenFile string) (*httpOfflineEvidenceVerifier, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
		return nil, fmt.Errorf("offline evidence verifier requires HTTPS or loopback HTTP")
	}
	tokenBytes, err := os.ReadFile(strings.TrimSpace(tokenFile))
	if err != nil || strings.TrimSpace(string(tokenBytes)) == "" {
		return nil, fmt.Errorf("read offline evidence verifier token: %w", err)
	}
	return &httpOfflineEvidenceVerifier{baseURL: parsed, token: strings.TrimSpace(string(tokenBytes)), client: &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("offline evidence redirects are forbidden")
		},
	}, now: time.Now}, nil
}

func (v *httpOfflineEvidenceVerifier) VerifyOfflineReservation(ctx context.Context, binding offline.Binding) error {
	if v == nil || v.baseURL == nil || v.client == nil || v.token == "" {
		return errors.New("offline evidence verifier unavailable")
	}
	endpoint := *v.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/v1/offline-reservations/" + url.PathEscape(binding.PlanID) + "/evidence"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+v.token)
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("evidence verifier status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, 16*1024+1)
	raw, err := io.ReadAll(limited)
	if err != nil || len(raw) > 16*1024 {
		return errors.New("invalid evidence response")
	}
	var evidence offlineEvidence
	if err := offlineReservationUniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return errors.New("invalid evidence response")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&evidence) != nil || decoder.Decode(&struct{}{}) != io.EOF || evidence.SchemaVersion != "norn.fleet-offline-independent-evidence/v1" {
		return errors.New("invalid evidence response")
	}
	now := v.now().UTC()
	if evidence.ObservedAt.IsZero() || evidence.ObservedAt.After(now.Add(time.Minute)) || now.Sub(evidence.ObservedAt) > 5*time.Minute ||
		evidence.PlanID != binding.PlanID || evidence.TargetID != binding.TargetID || evidence.Resource != binding.Resource ||
		evidence.SourceCommit != binding.SourceCommit || evidence.SavedPlanSHA256 != binding.SavedPlanSHA256 ||
		evidence.EffectSHA256 != binding.EffectSHA256 || evidence.Backend != binding.Backend ||
		evidence.ProviderAccountUUID != binding.ProviderAccountUUID || evidence.NodeName != binding.NodeName || evidence.DropletID != binding.DropletID {
		return errors.New("independent evidence differs from binding")
	}
	return nil
}
