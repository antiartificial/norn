package offline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const DeleteCommitmentSchema = "norn.fleet-offline-delete-commitment/v1"
const DeleteRetryPolicy = "never-reissue-after-commit"

var ErrDeleteCommitment = errors.New("offline delete commitment is invalid or mismatched")

// DeleteCommitment is the authority's proposed pre-dispatch ledger entry. A
// DigitalOcean droplet DELETE returns 204 with no action ID: this record binds
// the attempt to a supervised runtime, not to an invented provider operation.
// It grants no effect by itself; durable authority CAS and executor admission
// must precede any DELETE request.
type DeleteCommitment struct {
	SchemaVersion          string    `json:"schemaVersion"`
	PlanID                 string    `json:"planId"`
	AttemptID              string    `json:"attemptId"`
	GrantID                string    `json:"grantId"`
	BindingSHA256          string    `json:"bindingSHA256"`
	SourceCommit           string    `json:"sourceCommit"`
	SavedPlanSHA256        string    `json:"savedPlanSHA256"`
	EffectSHA256           string    `json:"effectSHA256"`
	ProviderAccountUUID    string    `json:"providerAccountUUID"`
	TargetID               string    `json:"targetId"`
	NodeName               string    `json:"nodeName"`
	DropletID              string    `json:"dropletId"`
	Backend                Backend   `json:"backend"`
	FenceGeneration        uint64    `json:"fenceGeneration"`
	AuthorityEpoch         int64     `json:"authorityEpoch"`
	RunnerPrincipalSubject string    `json:"runnerPrincipalSubject"`
	RunnerTokenID          string    `json:"runnerTokenId"`
	RunnerDeviceID         string    `json:"runnerDeviceId"`
	SupervisorRootID       string    `json:"supervisorRootId"`
	SupervisorExecutionID  string    `json:"supervisorExecutionId"`
	RuntimeInstanceID      string    `json:"runtimeInstanceId"`
	RetryPolicy            string    `json:"retryPolicy"`
	CommittedAt            time.Time `json:"committedAt"`
}

// NewDeleteCommitment constructs canonical ledger bytes from a consumed grant,
// admitted attempt, and the exact supervisor runtime that would send DELETE.
// Legacy v1 bindings are refused. This does not persist the record.
func NewDeleteCommitment(b Binding, a Attempt, g EffectGrant, rootID, executionID, runtimeID string, now time.Time) (DeleteCommitment, []byte, error) {
	if b.SchemaVersion != BindingSchemaV2 || b.Validate() != nil || a.SchemaVersion != AttemptSchema ||
		g.SchemaVersion != EffectGrantSchema || g.Phase != "consumed" || g.ConsumedAt == nil ||
		a.ID != g.AttemptID || a.PlanID != b.PlanID || g.PlanID != b.PlanID ||
		a.BindingSHA256 != g.BindingSHA256 || a.TargetID != b.TargetID ||
		a.FenceGeneration != b.FenceGeneration || g.TargetID != b.TargetID ||
		g.FenceGeneration != b.FenceGeneration || g.SourceCommit != b.SourceCommit ||
		g.SavedPlanSHA256 != b.SavedPlanSHA256 || g.EffectSHA256 != b.EffectSHA256 ||
		g.ProviderAccountUUID != b.ProviderAccountUUID || g.NodeName != b.NodeName ||
		g.DropletID != b.DropletID || g.Backend != b.Backend ||
		a.PrincipalSubject != b.RunnerPrincipalSubject ||
		now.IsZero() || now.Before(*g.ConsumedAt) || !now.Before(g.ExpiresAt) ||
		!now.Before(b.ExpiresAt) {
		return DeleteCommitment{}, nil, ErrDeleteCommitment
	}
	bindingBytes, err := json.Marshal(b)
	if err != nil || DeleteCommitmentSHA256(bindingBytes) != a.BindingSHA256 {
		return DeleteCommitment{}, nil, ErrDeleteCommitment
	}
	commitment := DeleteCommitment{
		SchemaVersion: DeleteCommitmentSchema, PlanID: b.PlanID, AttemptID: a.ID,
		GrantID: g.ID, BindingSHA256: g.BindingSHA256, SourceCommit: b.SourceCommit,
		SavedPlanSHA256: b.SavedPlanSHA256, EffectSHA256: b.EffectSHA256,
		ProviderAccountUUID: b.ProviderAccountUUID, TargetID: b.TargetID,
		NodeName: b.NodeName, DropletID: b.DropletID, Backend: b.Backend,
		FenceGeneration: b.FenceGeneration, AuthorityEpoch: g.AuthorityEpoch,
		RunnerPrincipalSubject: a.PrincipalSubject, RunnerTokenID: a.PrincipalTokenID,
		RunnerDeviceID: a.PrincipalDeviceID, SupervisorRootID: rootID,
		SupervisorExecutionID: executionID, RuntimeInstanceID: runtimeID,
		RetryPolicy: DeleteRetryPolicy, CommittedAt: now.UTC(),
	}
	if commitment.Validate() != nil {
		return DeleteCommitment{}, nil, ErrDeleteCommitment
	}
	raw, err := json.Marshal(commitment)
	return commitment, raw, err
}

func (c DeleteCommitment) Validate() error {
	if c.SchemaVersion != DeleteCommitmentSchema || c.RetryPolicy != DeleteRetryPolicy ||
		uuid.Validate(c.PlanID) != nil || uuid.Validate(c.AttemptID) != nil || uuid.Validate(c.GrantID) != nil ||
		!hex64.MatchString(c.BindingSHA256) || !hex40.MatchString(c.SourceCommit) ||
		!hex64.MatchString(c.SavedPlanSHA256) || !hex64.MatchString(c.EffectSHA256) ||
		c.ProviderAccountUUID == "" || c.TargetID == "" || c.NodeName != WorkNode || c.DropletID != WorkDropletID ||
		c.Backend.Bucket == "" || c.Backend.Key == "" || c.Backend.VersionID == "" ||
		c.Backend.Lineage == "" || c.Backend.Serial == 0 || !hex64.MatchString(c.Backend.RawSHA256) ||
		c.FenceGeneration == 0 || c.AuthorityEpoch <= 0 ||
		c.RunnerPrincipalSubject == "" || c.RunnerTokenID == "" || c.RunnerDeviceID == "" ||
		uuid.Validate(c.SupervisorRootID) != nil || uuid.Validate(c.SupervisorExecutionID) != nil ||
		uuid.Validate(c.RuntimeInstanceID) != nil || c.CommittedAt.IsZero() {
		return ErrDeleteCommitment
	}
	return nil
}

// DecodeDeleteCommitment accepts only the exact canonical bytes. An authority
// ledger should compare the digest of these bytes before executing or restoring.
func DecodeDeleteCommitment(raw []byte) (DeleteCommitment, error) {
	var c DeleteCommitment
	if len(raw) == 0 || len(raw) > 16*1024 || !json.Valid(raw) ||
		rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(raw))) != nil {
		return c, ErrDeleteCommitment
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || c.Validate() != nil {
		return DeleteCommitment{}, ErrDeleteCommitment
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(raw, canonical) {
		return DeleteCommitment{}, ErrDeleteCommitment
	}
	return c, nil
}

func DeleteCommitmentSHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
