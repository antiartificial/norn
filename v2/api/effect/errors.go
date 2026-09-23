package effect

import (
	"errors"
	"fmt"
)

var (
	ErrResourceBlocked = errors.New("external effect resource is blocked")
	ErrEffectPending   = errors.New("external effect remains unresolved")
	ErrStaleToken      = errors.New("external effect token is stale")
)

type ResourceBlockedError struct {
	Resource         string
	BlockingEffectID string
}

func (e *ResourceBlockedError) Error() string {
	if e.BlockingEffectID == "" {
		return fmt.Sprintf("external effect resource %s is blocked by unresolved work", e.Resource)
	}
	return fmt.Sprintf("external effect resource %s is blocked by effect %s", e.Resource, e.BlockingEffectID)
}

func (e *ResourceBlockedError) Unwrap() error { return ErrResourceBlocked }

type PendingError struct {
	EffectID string
	Resource string
	Reason   string
	Cause    error
}

func (e *PendingError) Error() string {
	message := fmt.Sprintf("external effect %s on %s remains unresolved", e.EffectID, e.Resource)
	if e.Reason != "" {
		message += ": " + e.Reason
	}
	return message
}

func (e *PendingError) Unwrap() error { return ErrEffectPending }

// ResolutionRecordedReason marks a pending result whose effect was durably
// resolved by verified evidence, so its resource gate is released.
const ResolutionRecordedReason = "verified resolution recorded; successor may reserve on the next claim"

func IsDeferred(err error) bool {
	return errors.Is(err, ErrResourceBlocked) || errors.Is(err, ErrEffectPending)
}
