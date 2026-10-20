package offline

import (
	"errors"
	"time"
)

const EffectGrantSchema = "norn.fleet-offline-effect-grant/v1"

var ErrEffectGrantMismatch = errors.New("offline effect grant differs from the held attempt")

// EffectGrant is a short-lived, one-use durable transition. This slice is
// intentionally inert: neither phase is a provider credential or permission
// to execute an external effect.
type EffectGrant struct {
	SchemaVersion        string     `json:"schemaVersion"`
	ID                   string     `json:"id"`
	PlanID               string     `json:"planId"`
	AttemptID            string     `json:"attemptId"`
	BindingSHA256        string     `json:"bindingSHA256"`
	TargetID             string     `json:"targetId"`
	FenceGeneration      uint64     `json:"fenceGeneration"`
	AuthorityEpoch       int64      `json:"authorityEpoch"`
	OwnerAuthorizationID string     `json:"ownerAuthorizationId"`
	SourceCommit         string     `json:"sourceCommit"`
	SavedPlanSHA256      string     `json:"savedPlanSHA256"`
	EffectSHA256         string     `json:"effectSHA256"`
	Backend              Backend    `json:"backend"`
	ProviderAccountUUID  string     `json:"providerAccountUUID"`
	NodeName             string     `json:"nodeName"`
	DropletID            string     `json:"dropletId"`
	RunnerSubject        string     `json:"runnerSubject"`
	RunnerTokenID        string     `json:"runnerTokenId"`
	RunnerDeviceID       string     `json:"runnerDeviceId"`
	Phase                string     `json:"phase"`
	AuthorizesEffect     bool       `json:"authorizesEffect"`
	IssuedAt             time.Time  `json:"issuedAt"`
	ExpiresAt            time.Time  `json:"expiresAt"`
	ConsumedAt           *time.Time `json:"consumedAt,omitempty"`
}
