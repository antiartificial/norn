package supervisor

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"norn/v2/api/effect"
)

const offlineDeleteDispatchIntentName = "offline-delete-dispatch-intent.json"

var errOfflineDeleteAlreadyDispatched = errors.New("offline delete dispatch was already attempted")

// offlineDeleteAdmission is the test-only authority-side, current-state check performed
// by the already launched supervised runtime. The exact canonical commitment
// bytes are passed through unchanged; the supervisor does not interpret or
// reconstruct authority evidence.
type offlineDeleteAdmission func(context.Context, effect.ExecutionIdentity, []byte) error

// offlineDelete invokes one local test double for the future provider DELETE.
// Production provider transport and credentials deliberately do not implement
// this local-only protocol yet.
type offlineDelete func(context.Context) (int, error)

type offlineDeleteDispatchResult struct {
	StatusCode int
}

type offlineDeleteDispatchIntent struct {
	SchemaVersion         string    `json:"schemaVersion"`
	CommitmentSHA256      string    `json:"commitmentSHA256"`
	Supervisor            string    `json:"supervisor"`
	SupervisorExecutionID string    `json:"supervisorExecutionId"`
	RuntimeInstanceID     string    `json:"runtimeInstanceId"`
	RecordedAt            time.Time `json:"recordedAt"`
}

type signedOfflineDeleteDispatchIntent struct {
	Intent offlineDeleteDispatchIntent `json:"intent"`
	MAC    string                      `json:"mac"`
}

// verifyRuntimeIdentity proves that identity belongs to the manager's signed
// registry and journal and that the backend still observes that exact runtime
// as running. It never treats a caller-supplied UUID as supervisor evidence.
func (m *Manager) verifyRuntimeIdentity(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity) error {
	if m == nil || identity.RuntimeInstanceID == "" {
		return fmt.Errorf("supervised runtime identity is incomplete")
	}
	return m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		if _, err := os.Lstat(filepath.Join(directory, "tombstone")); err == nil {
			return fmt.Errorf("supervisor execution is durably revoked")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		if record.RuntimeInstanceID == "" || record.Phase != "launched" {
			return fmt.Errorf("supervisor execution is not durably launched")
		}
		state, err := m.backend.Observe(ctx, backendExecution(record, directory))
		if err != nil {
			return err
		}
		if state.Phase != effect.SupervisorRunning {
			return fmt.Errorf("supervised runtime is not running")
		}
		return nil
	})
}

// verifyRuntimeBinding adapts commitment identifiers to the authority's narrow
// verifier callback without allowing the commitment to choose another local
// supervisor root or reservation namespace.
func (m *Manager) verifyRuntimeBinding(ctx context.Context, reservation effect.Reservation, rootID, executionID, runtimeID string) error {
	if m == nil || rootID == "" || rootID != m.RootID() || executionID != reservation.SupervisorExecutionID {
		return fmt.Errorf("supervised runtime binding does not match the local reservation")
	}
	return m.verifyRuntimeIdentity(ctx, reservation, effect.ExecutionIdentity{
		Supervisor: reservation.Supervisor, SupervisorExecutionID: executionID, RuntimeInstanceID: runtimeID,
	})
}

// dispatchOfflineDeleteOnce exercises fresh authority admission, then records a
// signed and fsynced dispatch intent before invoking the deleter. Once that
// marker exists this execution can never invoke DELETE again, including after
// an error or a lost response. Such outcomes require external reconciliation.
// It is deliberately package-private: authority admission and the local marker
// are not yet one atomic cross-process boundary, so this harness must not be
// wired to a provider transport.
func (m *Manager) dispatchOfflineDeleteOnce(ctx context.Context, reservation effect.Reservation, identity effect.ExecutionIdentity, commitment []byte, admit offlineDeleteAdmission, deleteDroplet offlineDelete) (offlineDeleteDispatchResult, error) {
	if m == nil || len(commitment) == 0 || admit == nil || deleteDroplet == nil {
		return offlineDeleteDispatchResult{}, fmt.Errorf("offline delete dispatch is incomplete")
	}
	digest := sha256.Sum256(commitment)
	// Surface a prior ambiguous dispatch before consulting authority again.
	// Expiry or revocation after a lost response must not hide the durable fact
	// that this runtime already crossed its one-shot boundary.
	if err := m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		return m.requireNoOfflineDeleteIntent(directory, identity, hex.EncodeToString(digest[:]))
	}); err != nil {
		return offlineDeleteDispatchResult{}, err
	}
	if err := admit(ctx, identity, commitment); err != nil {
		return offlineDeleteDispatchResult{}, err
	}
	var result offlineDeleteDispatchResult
	err := m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		if err := m.requireNoOfflineDeleteIntent(directory, identity, hex.EncodeToString(digest[:])); err != nil {
			return err
		}
		if _, err := os.Lstat(filepath.Join(directory, "tombstone")); err == nil {
			return fmt.Errorf("supervisor execution is durably revoked")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil {
			return err
		}
		if record.RuntimeInstanceID == "" || record.Phase != "launched" {
			return fmt.Errorf("supervisor execution is not durably launched")
		}
		state, err := m.backend.Observe(ctx, backendExecution(record, directory))
		if err != nil {
			return err
		}
		if state.Phase != effect.SupervisorRunning {
			return fmt.Errorf("supervised runtime is not running")
		}
		intent := offlineDeleteDispatchIntent{
			SchemaVersion: "norn.offline-delete-dispatch-intent/v1", CommitmentSHA256: hex.EncodeToString(digest[:]),
			Supervisor: identity.Supervisor, SupervisorExecutionID: identity.SupervisorExecutionID,
			RuntimeInstanceID: identity.RuntimeInstanceID, RecordedAt: time.Now().UTC(),
		}
		encoded, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if err := writeDurableJSON(directory, offlineDeleteDispatchIntentName, signedOfflineDeleteDispatchIntent{Intent: intent, MAC: m.mac(encoded)}); err != nil {
			return err
		}
		status, err := deleteDroplet(ctx)
		result.StatusCode = status
		if err != nil {
			return err
		}
		if status != 204 {
			return fmt.Errorf("offline delete returned status %d", status)
		}
		return nil
	})
	return result, err
}

func (m *Manager) requireNoOfflineDeleteIntent(directory string, identity effect.ExecutionIdentity, commitmentSHA256 string) error {
	path := filepath.Join(directory, offlineDeleteDispatchIntentName)
	data, err := readBoundedRegular(path, maxRunnerStatusBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("offline delete dispatch history is unavailable: %w", err)
	}
	var envelope signedOfflineDeleteDispatchIntent
	if decodeStrict(data, &envelope) != nil {
		return fmt.Errorf("offline delete dispatch history is malformed")
	}
	encoded, _ := json.Marshal(envelope.Intent)
	intent := envelope.Intent
	if !hmac.Equal([]byte(envelope.MAC), []byte(m.mac(encoded))) || intent.SchemaVersion != "norn.offline-delete-dispatch-intent/v1" ||
		intent.CommitmentSHA256 != commitmentSHA256 || intent.Supervisor != identity.Supervisor ||
		intent.SupervisorExecutionID != identity.SupervisorExecutionID || intent.RuntimeInstanceID != identity.RuntimeInstanceID ||
		intent.RecordedAt.IsZero() {
		return fmt.Errorf("offline delete dispatch history authentication failed")
	}
	return errOfflineDeleteAlreadyDispatched
}
