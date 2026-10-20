package observerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"norn/v2/api/fleet/offline"
)

var ErrSourceOrigin = errors.New("independent protected Fleet source is unavailable")

// ProtectedSourceReader has its own read-only GitHub App installation identity.
// The production githubapp.Client verifies protected Fleet main and an already
// successful exact-SHA validation run. The effect runner cannot supply proof.
type ProtectedSourceReader interface {
	ReadOfflineEffectSourceTranscript(context.Context, string) ([]byte, error)
}

type SourceOrigin struct {
	Reader ProtectedSourceReader
}

// ReadSource performs a fresh authenticated read and binds the redacted
// transcript to the target challenge and current attempt. It does not contact
// Actions to trigger a run; if Actions was unavailable for this commit, proof
// fails closed. The caller journals the returned exact bytes before signing.
func (s SourceOrigin) ReadSource(ctx context.Context, c offline.EffectChallengeV1, b offline.Binding, a offline.Attempt) ([]byte, error) {
	if s.Reader == nil || ctx.Err() != nil || b.Validate() != nil ||
		c.PlanID != b.PlanID || c.AttemptID != a.ID || c.BindingSHA256 != a.BindingSHA256 ||
		c.SourceCommit != b.SourceCommit || c.SavedPlanSHA256 != b.SavedPlanSHA256 ||
		c.EffectSHA256 != b.EffectSHA256 || c.TargetID != b.TargetID || c.FenceGeneration != b.FenceGeneration ||
		c.ProviderAccountUUID != b.ProviderAccountUUID || c.NodeName != b.NodeName || c.DropletID != b.DropletID ||
		a.PlanID != b.PlanID || a.TargetID != b.TargetID || a.FenceGeneration != b.FenceGeneration {
		return nil, ErrSourceOrigin
	}
	bindingRaw, err := json.Marshal(b)
	if err != nil {
		return nil, ErrSourceOrigin
	}
	bindingHash := sha256.Sum256(bindingRaw)
	if hex.EncodeToString(bindingHash[:]) != a.BindingSHA256 {
		return nil, ErrSourceOrigin
	}
	raw, err := s.Reader.ReadOfflineEffectSourceTranscript(ctx, b.SourceCommit)
	if err != nil || len(raw) == 0 || len(raw) > 16<<10 {
		return nil, ErrSourceOrigin
	}
	var proof struct {
		Schema        string `json:"schema"`
		Repository    string `json:"repository"`
		Branch        string `json:"branch"`
		Commit        string `json:"commit"`
		Protected     bool   `json:"protected"`
		ValidationRun int64  `json:"validationRun"`
	}
	if !uniqueSourceFields(raw) || json.Unmarshal(raw, &proof) != nil || proof.Schema != "norn.fleet-offline-effect-source/v1" ||
		proof.Repository != "antiartificial/norn-fleet" || proof.Branch != "main" ||
		proof.Commit != b.SourceCommit || !proof.Protected || proof.ValidationRun <= 0 {
		return nil, ErrSourceOrigin
	}
	transcript, err := json.Marshal(struct {
		Schema      string          `json:"schema"`
		ChallengeID string          `json:"challengeId"`
		PlanID      string          `json:"planId"`
		AttemptID   string          `json:"attemptId"`
		BindingSHA  string          `json:"bindingSHA256"`
		ObservedAt  time.Time       `json:"observedAt"`
		GitHubProof json.RawMessage `json:"githubProof"`
	}{"norn.fleet-offline-effect-source-observation/v1", c.ID, b.PlanID, a.ID, a.BindingSHA256, time.Now().UTC(), raw})
	if err != nil || len(transcript) > 16<<10 {
		return nil, ErrSourceOrigin
	}
	return transcript, nil
}

func uniqueSourceFields(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		keyToken, e := d.Token()
		key, ok := keyToken.(string)
		if e != nil || !ok || seen[key] {
			return false
		}
		switch key {
		case "schema", "repository", "branch", "commit", "protected", "validationRun":
		default:
			return false
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return false
		}
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') || len(seen) != 6 {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}
