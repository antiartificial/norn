package offline

// The Fleet effect-time receipt is a read-only assertion produced by the
// pinned runner. This parser binds its identities to the reservation; it does
// not attest its origin or authorize a provider effect. An effect lane must
// independently repeat live observations, including headroom and service health.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const PrechangeSchema = "norn.fleet-offline-contraction-effect-time/v1"

var ErrPrechangeEvidence = errors.New("offline prechange evidence does not match reservation")

type PrechangeSummary struct {
	SchemaVersion string    `json:"schemaVersion"`
	SHA256        string    `json:"sha256"`
	ObservedAt    time.Time `json:"observedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// ValidatePrechangeReceipt accepts the Fleet verifier's current receipt shape.
// It is deliberately short-lived and bound to one admitted attempt/fence.
func ValidatePrechangeReceipt(raw []byte, binding Binding, attempt Attempt, now time.Time) (PrechangeSummary, error) {
	if len(raw) == 0 || len(raw) > 32<<10 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil ||
		binding.Validate() != nil || attempt.PlanID != binding.PlanID ||
		attempt.TargetID != binding.TargetID || attempt.FenceGeneration != binding.FenceGeneration ||
		attempt.ID == "" || attempt.BindingSHA256 == "" || now.Before(binding.CreatedAt) || !now.Before(binding.ExpiresAt) {
		return PrechangeSummary{}, ErrPrechangeEvidence
	}
	var receipt struct {
		SchemaVersion    string    `json:"schemaVersion"`
		ObservedAt       time.Time `json:"observedAt"`
		ReadOnly         bool      `json:"readOnly"`
		AuthorizesEffect bool      `json:"authorizesEffect"`
		Plan             struct {
			SavedPlanSHA256 string `json:"savedPlanSHA256"`
			EffectSHA256    string `json:"effectSHA256"`
			TofuVersion     string `json:"tofuVersion"`
			Source          struct {
				Commit string `json:"commit"`
				Root   string `json:"root"`
			} `json:"source"`
		} `json:"plan"`
		Freshness struct {
			SchemaVersion string    `json:"schemaVersion"`
			ObservedAt    time.Time `json:"observedAt"`
			AccountUUID   string    `json:"accountUUID"`
			Backend       Backend   `json:"backend"`
			Nodes         []struct {
				Name       string `json:"name"`
				ProviderID string `json:"providerId"`
				Status     string `json:"status"`
			} `json:"nodes"`
			ReadOnly         bool `json:"readOnly"`
			AuthorizesEffect bool `json:"authorizesEffect"`
		} `json:"freshness"`
		Workload struct {
			Job                  string   `json:"job"`
			Namespace            string   `json:"namespace"`
			RunningAllocationIDs []string `json:"runningAllocationIDs"`
			SurvivingClientNames []string `json:"survivingClientNames"`
		} `json:"workload"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.SchemaVersion != PrechangeSchema ||
		!receipt.ReadOnly || receipt.AuthorizesEffect || receipt.Plan.TofuVersion != "1.10.6" ||
		receipt.Plan.Source.Commit != binding.SourceCommit || receipt.Plan.Source.Root != binding.Resource ||
		receipt.Plan.SavedPlanSHA256 != binding.SavedPlanSHA256 || receipt.Plan.EffectSHA256 != binding.EffectSHA256 ||
		receipt.Freshness.SchemaVersion != "norn.fleet-offline-contraction-freshness/v1" ||
		!receipt.Freshness.ReadOnly || receipt.Freshness.AuthorizesEffect ||
		receipt.Freshness.AccountUUID != binding.ProviderAccountUUID || receipt.Freshness.Backend != binding.Backend ||
		receipt.Workload.Job != "hello-norn-mysql" || receipt.Workload.Namespace != "default" ||
		len(receipt.Workload.RunningAllocationIDs) != 2 || receipt.Workload.RunningAllocationIDs[0] == "" ||
		receipt.Workload.RunningAllocationIDs[1] == "" || receipt.Workload.RunningAllocationIDs[0] == receipt.Workload.RunningAllocationIDs[1] ||
		len(receipt.Workload.SurvivingClientNames) != 2 || receipt.Workload.SurvivingClientNames[0] != "app-01" ||
		receipt.Workload.SurvivingClientNames[1] != "app-02" || len(receipt.Freshness.Nodes) != 8 {
		return PrechangeSummary{}, ErrPrechangeEvidence
	}
	found := false
	names := map[string]bool{}
	for _, node := range receipt.Freshness.Nodes {
		if node.Name == "" || names[node.Name] || node.ProviderID == "" || node.Status != "active" {
			return PrechangeSummary{}, ErrPrechangeEvidence
		}
		names[node.Name] = true
		if node.Name == binding.NodeName && node.ProviderID == binding.DropletID && node.Status == "active" {
			found = true
		}
	}
	for _, expected := range []string{"app-01", "app-02", "app-03", "control-01", "control-02", "control-03", "ingress-01", "ingress-02"} {
		if !names[expected] {
			return PrechangeSummary{}, ErrPrechangeEvidence
		}
	}
	if !found || receipt.ObservedAt.IsZero() || receipt.Freshness.ObservedAt.IsZero() ||
		receipt.ObservedAt.Before(receipt.Freshness.ObservedAt) || now.Before(receipt.ObservedAt) ||
		now.Before(receipt.Freshness.ObservedAt) || now.Sub(receipt.ObservedAt) > 5*time.Minute ||
		now.Sub(receipt.Freshness.ObservedAt) > 5*time.Minute {
		return PrechangeSummary{}, ErrPrechangeEvidence
	}
	hash := sha256.Sum256(raw)
	return PrechangeSummary{SchemaVersion: PrechangeSchema, SHA256: hex.EncodeToString(hash[:]),
		ObservedAt: receipt.ObservedAt, ExpiresAt: receipt.ObservedAt.Add(5 * time.Minute)}, nil
}
