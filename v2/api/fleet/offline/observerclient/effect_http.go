package observerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"norn/v2/api/etcdstore"
	"norn/v2/api/fleet/offline"
)

// EffectHTTPClient uses the same pinned IP, TLS identity, and observer-only
// bearer as HTTPClient. It has no verifier or runner mutation method.
type EffectHTTPClient struct {
	terminal *HTTPClient
	mu       sync.Mutex
	paths    map[string]string
}

var _ EffectClient = (*EffectHTTPClient)(nil)

func NewEffectHTTPClient(config HTTPConfig) (*EffectHTTPClient, error) {
	// Effect receipts can authorize later provider effects. They must be
	// authenticated by a dedicated target CA, never ambient system roots.
	if config.RootCAs == nil {
		return nil, errors.New("effect observer target requires dedicated CA roots")
	}
	config.RootCAs = config.RootCAs.Clone()
	client, err := NewHTTPClient(config)
	if err != nil {
		return nil, err
	}
	return &EffectHTTPClient{terminal: client, paths: make(map[string]string)}, nil
}

func effectPath(plan, attempt, purpose string) (string, error) {
	base, err := attemptPath(plan, attempt)
	if err != nil || (purpose != offline.EffectIssuePurpose && purpose != offline.EffectConsumePurpose) {
		return "", offline.ErrEffectAttestation
	}
	return base + "/signed-effect/" + purpose + "/observer", nil
}

func effectReceiptKey(plan, attempt, challenge string) string {
	return plan + "/" + attempt + "/" + challenge
}

func (c *EffectHTTPClient) ReadEffectChallenge(ctx context.Context, plan, attempt, purpose string) (offline.EffectChallengeV1, error) {
	var response struct {
		Challenge offline.EffectChallengeV1 `json:"challenge"`
	}
	path, err := effectPath(plan, attempt, purpose)
	if err != nil {
		return offline.EffectChallengeV1{}, err
	}
	if err = c.callEffect(ctx, http.MethodGet, path+"/challenge", nil, &response); err != nil {
		return offline.EffectChallengeV1{}, err
	}
	challenge := response.Challenge
	if challenge.SchemaVersion != offline.EffectChallengeSchemaV1 || challenge.PlanID != plan || challenge.AttemptID != attempt ||
		challenge.Purpose != purpose || !exactEffectUUID(challenge.ID) {
		return offline.EffectChallengeV1{}, offline.ErrEffectAttestation
	}
	c.mu.Lock()
	c.paths[effectReceiptKey(plan, attempt, challenge.ID)] = purpose
	c.mu.Unlock()
	return challenge, nil
}

func (c *EffectHTTPClient) ReadAcceptedEffectAttestation(ctx context.Context, plan, attempt, challengeID string) (AcceptedEffectAttestation, error) {
	if c == nil || !exactEffectUUID(challengeID) {
		return AcceptedEffectAttestation{}, offline.ErrEffectAttestation
	}
	c.mu.Lock()
	purpose := c.paths[effectReceiptKey(plan, attempt, challengeID)]
	c.mu.Unlock()
	path, err := effectPath(plan, attempt, purpose)
	if err != nil {
		return AcceptedEffectAttestation{}, err
	}
	var response struct {
		Accepted etcdstore.AcceptedOfflineEffectAttestation `json:"accepted"`
	}
	if err := c.callEffect(ctx, http.MethodGet, path+"/attestations/"+challengeID, nil, &response); err != nil {
		return AcceptedEffectAttestation{}, err
	}
	return decodeEffectReceipt(response.Accepted, plan, attempt, purpose, challengeID)
}

func (c *EffectHTTPClient) SubmitEffectAttestation(ctx context.Context, plan, attempt string, signed offline.SignedEffectAttestationV1) error {
	a := signed.Attestation
	if a.Observation.PlanID != plan || a.Observation.AttemptID != attempt || !exactEffectUUID(a.ChallengeID) {
		return offline.ErrEffectAttestation
	}
	path, err := effectPath(plan, attempt, a.Purpose)
	if err != nil || c == nil {
		return offline.ErrEffectAttestation
	}
	c.mu.Lock()
	knownPurpose := c.paths[effectReceiptKey(plan, attempt, a.ChallengeID)]
	c.mu.Unlock()
	if knownPurpose != a.Purpose {
		return offline.ErrEffectAttestation
	}
	body, err := json.Marshal(signed)
	if err != nil || len(body) > 32<<10 {
		return offline.ErrEffectAttestation
	}
	var response struct {
		Accepted etcdstore.AcceptedOfflineEffectAttestation `json:"accepted"`
	}
	// This is one request only. A lost response is reconciled by exact GET,
	// never by repeating a potentially committed PUT.
	if err := c.callEffect(ctx, http.MethodPut, path+"/attestations/"+a.ChallengeID, body, &response); err != nil {
		return err
	}
	accepted, err := decodeEffectReceipt(response.Accepted, plan, attempt, a.Purpose, a.ChallengeID)
	if err != nil || accepted.Signed != signed {
		return offline.ErrEffectAttestation
	}
	return nil
}

func decodeEffectReceipt(receipt etcdstore.AcceptedOfflineEffectAttestation, plan, attempt, purpose, challengeID string) (AcceptedEffectAttestation, error) {
	if receipt.SchemaVersion != "norn.fleet-offline-effect-accepted/v1" || receipt.PlanID != plan || receipt.AttemptID != attempt ||
		receipt.Purpose != purpose || receipt.ChallengeID != challengeID || receipt.AcceptedAt.IsZero() ||
		receipt.Challenge.ID != challengeID || receipt.Challenge.PlanID != plan || receipt.Challenge.AttemptID != attempt || receipt.Challenge.Purpose != purpose {
		return AcceptedEffectAttestation{}, offline.ErrEffectAttestation
	}
	signed, err := offline.DecodeSignedEffectAttestationV1(receipt.Envelope)
	sum := sha256.Sum256(receipt.Envelope)
	if err != nil || receipt.AttestationSHA256 != hex.EncodeToString(sum[:]) || signed.Attestation.ChallengeID != challengeID ||
		signed.Attestation.Purpose != purpose || signed.Attestation.Observation.PlanID != plan || signed.Attestation.Observation.AttemptID != attempt {
		return AcceptedEffectAttestation{}, offline.ErrEffectAttestation
	}
	return AcceptedEffectAttestation{Signed: signed, AcceptedAt: receipt.AcceptedAt}, nil
}

func exactEffectUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func (c *EffectHTTPClient) callEffect(ctx context.Context, method, path string, body []byte, dest any) error {
	if c == nil || c.terminal == nil || c.terminal.client == nil {
		return errors.New("effect observer HTTP client is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.terminal.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.terminal.observerBearer)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.terminal.client.Do(req)
	if err != nil {
		return fmt.Errorf("effect observer target request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if method == http.MethodGet && resp.StatusCode == http.StatusNotFound &&
			resp.Header.Get("Content-Type") == "application/problem+json" && resp.Header.Get("Cache-Control") == "private, no-store" && resp.Header.Get("Content-Encoding") == "" {
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
			var problem struct {
				Type      string `json:"type"`
				Title     string `json:"title"`
				Status    int    `json:"status"`
				Code      string `json:"code"`
				Detail    string `json:"detail,omitempty"`
				RequestID string `json:"requestId,omitempty"`
				Error     string `json:"error,omitempty"`
			}
			if readErr == nil && len(raw) <= 4096 && strictEffectJSON(raw, &problem) == nil &&
				problem.Type == "https://norn.dev/problems/offline_effect_not_found" && problem.Title == "Not Found" &&
				problem.Status == http.StatusNotFound && problem.Code == "offline_effect_not_found" &&
				problem.Detail == "signed effect state is absent or inconsistent" && problem.Error == problem.Detail {
				return ErrNotFound
			}
		}
		return fmt.Errorf("effect observer target rejected %s: HTTP %d", method, resp.StatusCode)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "private, no-store" || resp.Header.Get("Content-Encoding") != "" {
		return errors.New("effect observer target returned unexpected response headers")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10+1))
	if err != nil || len(raw) == 0 || len(raw) > 256<<10 || strictEffectJSON(raw, dest) != nil {
		return errors.New("effect observer target returned invalid or ambiguous JSON")
	}
	return nil
}

func strictEffectJSON(raw []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	canonical, err := json.Marshal(dest)
	if err != nil || !bytes.Equal(bytes.TrimSuffix(raw, []byte("\n")), canonical) {
		return offline.ErrEffectAttestation
	}
	return nil
}
