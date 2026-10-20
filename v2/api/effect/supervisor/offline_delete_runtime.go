package supervisor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"norn/v2/api/effect"
)

const offlineDeleteRuntimeBindingSchema = "norn.offline-delete-runtime-binding/v1"

// OfflineDeleteRuntimeIdentity is a read-only result derived from the signed
// supervisor registry, journal and the process backend's current observation.
type OfflineDeleteRuntimeIdentity struct {
	SupervisorRootID      string
	SupervisorExecutionID string
	RuntimeInstanceID     string
}

type offlineDeleteRuntimeBinding struct {
	SchemaVersion string             `json:"schemaVersion"`
	PlanID        string             `json:"planId"`
	AttemptID     string             `json:"attemptId"`
	GrantID       string             `json:"grantId"`
	Reservation   effect.Reservation `json:"reservation"`
}

type signedOfflineDeleteRuntimeBinding struct {
	Binding offlineDeleteRuntimeBinding `json:"binding"`
	MAC     string                      `json:"mac"`
}

func offlineDeleteRuntimeBindingName(planID, attemptID, grantID string) string {
	digest := sha256.Sum256([]byte(planID + "\x00" + attemptID + "\x00" + grantID))
	return "offline-delete-runtime-" + hex.EncodeToString(digest[:]) + ".json"
}

// BindOfflineDeleteRuntime durably indexes an already registered and launched
// offline-delete reservation. It does not create or launch an execution.
func (m *Manager) BindOfflineDeleteRuntime(ctx context.Context, planID, attemptID, grantID string, reservation effect.Reservation) error {
	if m == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil ||
		reservation.Stage != "offline.delete" || strings.TrimSpace(reservation.SupervisorExecutionID) == "" {
		return fmt.Errorf("offline delete runtime binding is invalid")
	}
	if digest, err := effect.ComputeInputDigest(reservation); err != nil || digest != reservation.InputDigest {
		return fmt.Errorf("offline delete reservation digest is invalid")
	}
	entry, err := m.registryEntry(reservation.SupervisorExecutionID)
	if err != nil || entry == nil || entry.RuntimeInstanceID == "" {
		return fmt.Errorf("offline delete runtime is not durably registered and launched")
	}
	identity := effect.ExecutionIdentity{Supervisor: reservation.Supervisor, SupervisorExecutionID: reservation.SupervisorExecutionID, RuntimeInstanceID: entry.RuntimeInstanceID}
	if err := m.verifyRuntimeIdentity(ctx, reservation, identity); err != nil {
		return err
	}
	binding := offlineDeleteRuntimeBinding{SchemaVersion: offlineDeleteRuntimeBindingSchema, PlanID: planID, AttemptID: attemptID, GrantID: grantID, Reservation: reservation}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	path := filepath.Join(m.root, offlineDeleteRuntimeBindingName(planID, attemptID, grantID))
	sealed, err := json.Marshal(signedOfflineDeleteRuntimeBinding{Binding: binding, MAC: m.mac(encoded)})
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(m.root, ".offline-delete-runtime-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(sealed)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// A same-filesystem hard link publishes the fully synced private temp file
	// atomically and refuses to replace any earlier authenticated binding.
	if err = os.Link(temporaryName, path); err != nil {
		if os.IsExist(err) {
			existing, readErr := m.readOfflineDeleteRuntimeBinding(planID, attemptID, grantID)
			existingReservation, _ := json.Marshal(existing.Reservation)
			requestedReservation, _ := json.Marshal(reservation)
			resolved, resolveErr := m.ResolveOfflineDeleteRuntime(ctx, planID, attemptID, grantID)
			if readErr == nil && bytes.Equal(existingReservation, requestedReservation) && resolveErr == nil && resolved.SupervisorExecutionID == reservation.SupervisorExecutionID && resolved.RuntimeInstanceID == identity.RuntimeInstanceID {
				return nil
			}
		}
		return fmt.Errorf("record offline delete runtime binding: %w", err)
	}
	return syncDirectory(m.root)
}

// ResolveOfflineDeleteRuntime revalidates the exact durable binding, signed
// registry and journal, plus the backend's current running state. It never
// accepts an execution or runtime identifier from the caller.
func (m *Manager) ResolveOfflineDeleteRuntime(ctx context.Context, planID, attemptID, grantID string) (OfflineDeleteRuntimeIdentity, error) {
	var zero OfflineDeleteRuntimeIdentity
	if m == nil || uuid.Validate(planID) != nil || uuid.Validate(attemptID) != nil || uuid.Validate(grantID) != nil {
		return zero, fmt.Errorf("offline delete runtime lookup is invalid")
	}
	binding, err := m.readOfflineDeleteRuntimeBinding(planID, attemptID, grantID)
	if err != nil {
		return zero, err
	}
	if digest, digestErr := effect.ComputeInputDigest(binding.Reservation); digestErr != nil || digest != binding.Reservation.InputDigest {
		return zero, fmt.Errorf("offline delete reservation digest is invalid")
	}
	entry, err := m.registryEntry(binding.Reservation.SupervisorExecutionID)
	if err != nil || entry == nil || entry.RuntimeInstanceID == "" {
		return zero, fmt.Errorf("offline delete runtime is not durably registered and launched")
	}
	identity := effect.ExecutionIdentity{Supervisor: binding.Reservation.Supervisor, SupervisorExecutionID: binding.Reservation.SupervisorExecutionID, RuntimeInstanceID: entry.RuntimeInstanceID}
	if err := m.verifyRuntimeIdentity(ctx, binding.Reservation, identity); err != nil {
		return zero, err
	}
	return OfflineDeleteRuntimeIdentity{SupervisorRootID: m.RootID(), SupervisorExecutionID: identity.SupervisorExecutionID, RuntimeInstanceID: identity.RuntimeInstanceID}, nil
}

func (m *Manager) readOfflineDeleteRuntimeBinding(planID, attemptID, grantID string) (offlineDeleteRuntimeBinding, error) {
	var zero offlineDeleteRuntimeBinding
	path := filepath.Join(m.root, offlineDeleteRuntimeBindingName(planID, attemptID, grantID))
	raw, err := readBoundedRegular(path, maxRunnerStatusBytes)
	if err != nil {
		return zero, fmt.Errorf("offline delete runtime binding is unavailable: %w", err)
	}
	var sealed signedOfflineDeleteRuntimeBinding
	if decodeStrict(raw, &sealed) != nil {
		return zero, fmt.Errorf("offline delete runtime binding is malformed")
	}
	encoded, _ := json.Marshal(sealed.Binding)
	binding := sealed.Binding
	if !hmac.Equal([]byte(sealed.MAC), []byte(m.mac(encoded))) || binding.SchemaVersion != offlineDeleteRuntimeBindingSchema ||
		binding.PlanID != planID || binding.AttemptID != attemptID || binding.GrantID != grantID || binding.Reservation.Stage != "offline.delete" {
		return zero, fmt.Errorf("offline delete runtime binding authentication failed")
	}
	return binding, nil
}
