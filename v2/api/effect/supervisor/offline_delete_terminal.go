package supervisor

import (
	"bytes"
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
	"norn/v2/api/fleet/offline"
)

const offlineDeleteTerminalName = "offline-delete-terminal.json"
const offlineDeleteTerminalSchema = "norn.offline-delete-terminal/v1"

// offlineDeleteRequest is the only request shape accepted by this offline
// protocol. Its digest includes the provider account and immutable droplet ID.
// A production transport must independently enforce the same destination.
type offlineDeleteRequest struct {
	Method              string `json:"method"`
	Path                string `json:"path"`
	ProviderAccountUUID string `json:"providerAccountUUID"`
	DropletID           string `json:"dropletId"`
}

func canonicalOfflineDeleteRequest(c offline.DeleteCommitment) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(offlineDeleteRequest{
		Method: "DELETE", Path: "/v2/droplets/" + c.DropletID,
		ProviderAccountUUID: c.ProviderAccountUUID, DropletID: c.DropletID,
	})
}

// offlineDeleteChildObservation is supplied by a local test reader, not by a
// provider response or the caller asking for release. A real implementation
// must authenticate these facts against the contained child and credential
// broker before this diagnostic protocol can be promoted to live use.
type offlineDeleteChildObservation struct {
	ResponseStatus           int
	ExitCode                 int
	ChildExited              bool
	ContainmentProven        bool
	CredentialRevoked        bool
	RestartDisabled          bool
	ChildEvidenceSHA256      string
	CredentialEvidenceSHA256 string
	ObservedAt               time.Time
}

type offlineDeleteChildReader func(context.Context, effect.ExecutionIdentity) (offlineDeleteChildObservation, error)

// offlineDeleteTerminal is an authenticated local diagnostic record. It does
// not assert provider absence, state continuity, or permission to release the
// Fleet writer fence.
type offlineDeleteTerminal struct {
	SchemaVersion            string    `json:"schemaVersion"`
	DispatchSHA256           string    `json:"dispatchSHA256"`
	CommitmentSHA256         string    `json:"commitmentSHA256"`
	RequestSHA256            string    `json:"requestSHA256"`
	ProviderAccountUUID      string    `json:"providerAccountUUID"`
	DropletID                string    `json:"dropletId"`
	SupervisorRootID         string    `json:"supervisorRootId"`
	SupervisorExecutionID    string    `json:"supervisorExecutionId"`
	RuntimeInstanceID        string    `json:"runtimeInstanceId"`
	ResponseStatus           int       `json:"responseStatus"`
	ExitCode                 int       `json:"exitCode"`
	ChildEvidenceSHA256      string    `json:"childEvidenceSHA256"`
	CredentialEvidenceSHA256 string    `json:"credentialEvidenceSHA256"`
	ChildExited              bool      `json:"childExited"`
	ContainmentProven        bool      `json:"containmentProven"`
	CredentialRevoked        bool      `json:"credentialRevoked"`
	RestartDisabled          bool      `json:"restartDisabled"`
	ObservedAt               time.Time `json:"observedAt"`
}

type signedOfflineDeleteTerminal struct {
	Terminal offlineDeleteTerminal `json:"terminal"`
	MAC      string                `json:"mac"`
}

type offlineDeleteDispatchBinding struct {
	SchemaVersion         string    `json:"schemaVersion"`
	PlanID                string    `json:"planId"`
	AttemptID             string    `json:"attemptId"`
	GrantID               string    `json:"grantId"`
	CommitmentSHA256      string    `json:"commitmentSHA256"`
	TargetID              string    `json:"targetId"`
	DropletID             string    `json:"dropletId"`
	FenceGeneration       uint64    `json:"fenceGeneration"`
	AuthorityEpoch        int64     `json:"authorityEpoch"`
	SupervisorRootID      string    `json:"supervisorRootId"`
	SupervisorExecutionID string    `json:"supervisorExecutionId"`
	RuntimeInstanceID     string    `json:"runtimeInstanceId"`
	State                 string    `json:"state"`
	RetryPolicy           string    `json:"retryPolicy"`
	AdmittedAt            time.Time `json:"admittedAt"`
}

func offlineDeleteSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func decodeOfflineDeleteDispatch(raw []byte, commitment []byte, c offline.DeleteCommitment) error {
	if len(raw) == 0 || len(raw) > 16*1024 {
		return fmt.Errorf("offline delete dispatch reservation is absent or oversized")
	}
	var d offlineDeleteDispatchBinding
	if decodeStrict(raw, &d) != nil {
		return fmt.Errorf("offline delete dispatch reservation is malformed")
	}
	canonical, err := json.Marshal(d)
	if err != nil || !bytes.Equal(canonical, raw) ||
		d.SchemaVersion != "norn.fleet-offline-delete-dispatch/v1" || d.State != "admitted" ||
		d.RetryPolicy != offline.DeleteRetryPolicy || d.AdmittedAt.IsZero() ||
		d.PlanID != c.PlanID || d.AttemptID != c.AttemptID || d.GrantID != c.GrantID ||
		d.CommitmentSHA256 != offlineDeleteSHA(commitment) ||
		d.TargetID != c.TargetID || d.DropletID != c.DropletID ||
		d.FenceGeneration != c.FenceGeneration || d.AuthorityEpoch != c.AuthorityEpoch ||
		d.SupervisorRootID != c.SupervisorRootID || d.SupervisorExecutionID != c.SupervisorExecutionID ||
		d.RuntimeInstanceID != c.RuntimeInstanceID {
		return fmt.Errorf("offline delete dispatch reservation differs from commitment")
	}
	return nil
}

// recordOfflineDeleteTerminal reads an already exited fake child, authenticates
// its bound intent, and persists one sealed marker. It cannot be used to send
// DELETE. A missing/ambiguous response, running child, or unrevoked credential
// leaves only the dispatch intent and cannot become terminal success.
func (m *Manager) recordOfflineDeleteTerminal(ctx context.Context, reservation effect.Reservation,
	identity effect.ExecutionIdentity, exactDispatch, exactCommitment []byte, reader offlineDeleteChildReader) (offlineDeleteTerminal, error) {
	if m == nil || reader == nil {
		return offlineDeleteTerminal{}, fmt.Errorf("offline delete terminal reader is unavailable")
	}
	c, err := offline.DecodeDeleteCommitment(exactCommitment)
	if err != nil || decodeOfflineDeleteDispatch(exactDispatch, exactCommitment, c) != nil ||
		c.SupervisorRootID != m.RootID() || c.SupervisorExecutionID != reservation.SupervisorExecutionID ||
		c.SupervisorExecutionID != identity.SupervisorExecutionID || c.RuntimeInstanceID != identity.RuntimeInstanceID ||
		identity.Supervisor != reservation.Supervisor {
		return offlineDeleteTerminal{}, fmt.Errorf("offline delete terminal binding is invalid")
	}
	request, err := canonicalOfflineDeleteRequest(c)
	if err != nil {
		return offlineDeleteTerminal{}, err
	}
	var terminal offlineDeleteTerminal
	err = m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		if _, err := os.Lstat(filepath.Join(directory, offlineDeleteTerminalName)); err == nil {
			return fmt.Errorf("offline delete terminal marker already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		record, err := m.readBoundJournal(directory, reservation, identity)
		if err != nil || record.Phase != "launched" {
			return fmt.Errorf("offline delete terminal has no launched supervisor journal: %w", err)
		}
		intentData, err := readBoundedRegular(filepath.Join(directory, offlineDeleteDispatchIntentName), maxRunnerStatusBytes)
		if err != nil {
			return fmt.Errorf("offline delete dispatch intent unavailable: %w", err)
		}
		var intent signedOfflineDeleteDispatchIntent
		if decodeStrict(intentData, &intent) != nil {
			return fmt.Errorf("offline delete dispatch intent malformed")
		}
		encoded, _ := json.Marshal(intent.Intent)
		if !hmac.Equal([]byte(intent.MAC), []byte(m.mac(encoded))) ||
			intent.Intent.SchemaVersion != "norn.offline-delete-dispatch-intent/v1" ||
			intent.Intent.CommitmentSHA256 != offlineDeleteSHA(exactCommitment) ||
			intent.Intent.Supervisor != identity.Supervisor ||
			intent.Intent.SupervisorExecutionID != identity.SupervisorExecutionID ||
			intent.Intent.RuntimeInstanceID != identity.RuntimeInstanceID || intent.Intent.RecordedAt.IsZero() {
			return fmt.Errorf("offline delete dispatch intent authentication failed")
		}
		receiptAt, err := m.requireOfflineDeleteResponseReceipt(directory, identity, exactCommitment, intent.Intent.RecordedAt)
		if err != nil {
			return err
		}
		observation, err := reader(ctx, identity)
		if err != nil {
			return err
		}
		if observation.ResponseStatus != 204 || observation.ExitCode != 0 ||
			!observation.ChildExited || !observation.ContainmentProven ||
			!observation.CredentialRevoked || !observation.RestartDisabled ||
			!validOfflineDeleteSHA(observation.ChildEvidenceSHA256) ||
			!validOfflineDeleteSHA(observation.CredentialEvidenceSHA256) ||
			observation.ObservedAt.IsZero() || observation.ObservedAt.Before(receiptAt) {
			return fmt.Errorf("offline delete child outcome is not safely terminal")
		}
		terminal = offlineDeleteTerminal{
			SchemaVersion: offlineDeleteTerminalSchema, DispatchSHA256: offlineDeleteSHA(exactDispatch),
			CommitmentSHA256: offlineDeleteSHA(exactCommitment), RequestSHA256: offlineDeleteSHA(request),
			ProviderAccountUUID: c.ProviderAccountUUID, DropletID: c.DropletID,
			SupervisorRootID: c.SupervisorRootID, SupervisorExecutionID: identity.SupervisorExecutionID,
			RuntimeInstanceID: identity.RuntimeInstanceID, ResponseStatus: observation.ResponseStatus,
			ExitCode: observation.ExitCode, ChildEvidenceSHA256: observation.ChildEvidenceSHA256,
			CredentialEvidenceSHA256: observation.CredentialEvidenceSHA256,
			ChildExited:              observation.ChildExited, ContainmentProven: observation.ContainmentProven,
			CredentialRevoked: observation.CredentialRevoked, RestartDisabled: observation.RestartDisabled,
			ObservedAt: observation.ObservedAt.UTC(),
		}
		encoded, err = json.Marshal(terminal)
		if err != nil {
			return err
		}
		return writeOfflineDeleteTerminalOnce(directory, signedOfflineDeleteTerminal{Terminal: terminal, MAC: m.mac(encoded)})
	})
	return terminal, err
}

func validOfflineDeleteSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

// readOfflineDeleteTerminal rechecks the durable supervisor journal and signed
// marker after restart. It does not re-read a mutable child or promote provider
// absence; callers must independently prove all Fleet postconditions.
func (m *Manager) readOfflineDeleteTerminal(reservation effect.Reservation, identity effect.ExecutionIdentity,
	exactDispatch, exactCommitment []byte) (offlineDeleteTerminal, error) {
	if m == nil {
		return offlineDeleteTerminal{}, fmt.Errorf("offline delete supervisor is unavailable")
	}
	c, err := offline.DecodeDeleteCommitment(exactCommitment)
	if err != nil || decodeOfflineDeleteDispatch(exactDispatch, exactCommitment, c) != nil ||
		c.SupervisorRootID != m.RootID() || c.SupervisorExecutionID != identity.SupervisorExecutionID ||
		c.RuntimeInstanceID != identity.RuntimeInstanceID || c.SupervisorExecutionID != reservation.SupervisorExecutionID ||
		identity.Supervisor != reservation.Supervisor {
		return offlineDeleteTerminal{}, fmt.Errorf("offline delete terminal binding is invalid")
	}
	request, err := canonicalOfflineDeleteRequest(c)
	if err != nil {
		return offlineDeleteTerminal{}, err
	}
	var terminal offlineDeleteTerminal
	err = m.withExecutionLock(identity.SupervisorExecutionID, func(directory string) error {
		if _, err := m.readBoundJournal(directory, reservation, identity); err != nil {
			return err
		}
		// A copied marker without its original one-shot intent is incomplete
		// history, even when the marker's own MAC is valid.
		intentData, err := readBoundedRegular(filepath.Join(directory, offlineDeleteDispatchIntentName), maxRunnerStatusBytes)
		if err != nil {
			return err
		}
		var intent signedOfflineDeleteDispatchIntent
		if decodeStrict(intentData, &intent) != nil {
			return fmt.Errorf("offline delete dispatch intent malformed")
		}
		intentBytes, _ := json.Marshal(intent.Intent)
		if !hmac.Equal([]byte(intent.MAC), []byte(m.mac(intentBytes))) ||
			intent.Intent.SchemaVersion != "norn.offline-delete-dispatch-intent/v1" ||
			intent.Intent.CommitmentSHA256 != offlineDeleteSHA(exactCommitment) ||
			intent.Intent.Supervisor != identity.Supervisor ||
			intent.Intent.SupervisorExecutionID != identity.SupervisorExecutionID ||
			intent.Intent.RuntimeInstanceID != identity.RuntimeInstanceID || intent.Intent.RecordedAt.IsZero() {
			return fmt.Errorf("offline delete dispatch intent authentication failed")
		}
		receiptAt, err := m.requireOfflineDeleteResponseReceipt(directory, identity, exactCommitment, intent.Intent.RecordedAt)
		if err != nil {
			return err
		}
		data, err := readBoundedRegular(filepath.Join(directory, offlineDeleteTerminalName), maxRunnerStatusBytes)
		if err != nil {
			return err
		}
		var sealed signedOfflineDeleteTerminal
		if decodeStrict(data, &sealed) != nil {
			return fmt.Errorf("offline delete terminal marker malformed")
		}
		encoded, _ := json.Marshal(sealed.Terminal)
		t := sealed.Terminal
		if !hmac.Equal([]byte(sealed.MAC), []byte(m.mac(encoded))) ||
			t.SchemaVersion != offlineDeleteTerminalSchema || t.DispatchSHA256 != offlineDeleteSHA(exactDispatch) ||
			t.CommitmentSHA256 != offlineDeleteSHA(exactCommitment) || t.RequestSHA256 != offlineDeleteSHA(request) ||
			t.ProviderAccountUUID != c.ProviderAccountUUID || t.DropletID != c.DropletID ||
			t.SupervisorRootID != c.SupervisorRootID || t.SupervisorExecutionID != identity.SupervisorExecutionID ||
			t.RuntimeInstanceID != identity.RuntimeInstanceID || t.ResponseStatus != 204 || t.ExitCode != 0 ||
			!t.ChildExited || !t.ContainmentProven || !t.CredentialRevoked || !t.RestartDisabled ||
			!validOfflineDeleteSHA(t.ChildEvidenceSHA256) || !validOfflineDeleteSHA(t.CredentialEvidenceSHA256) ||
			t.ObservedAt.IsZero() || t.ObservedAt.Before(receiptAt) {
			return fmt.Errorf("offline delete terminal marker authentication failed")
		}
		terminal = t
		return nil
	})
	return terminal, err
}

func (m *Manager) requireOfflineDeleteResponseReceipt(directory string, identity effect.ExecutionIdentity, exactCommitment []byte, intentAt time.Time) (time.Time, error) {
	data, err := readBoundedRegular(filepath.Join(directory, offlineDeleteResponseReceiptName), maxRunnerStatusBytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("offline delete 204 receipt unavailable: %w", err)
	}
	var sealed signedOfflineDeleteResponseReceipt
	if decodeStrict(data, &sealed) != nil {
		return time.Time{}, fmt.Errorf("offline delete 204 receipt malformed")
	}
	encoded, _ := json.Marshal(sealed.Receipt)
	r := sealed.Receipt
	if !hmac.Equal([]byte(sealed.MAC), []byte(m.mac(encoded))) ||
		r.SchemaVersion != "norn.offline-delete-response-receipt/v1" ||
		r.CommitmentSHA256 != offlineDeleteSHA(exactCommitment) ||
		r.SupervisorExecutionID != identity.SupervisorExecutionID || r.RuntimeInstanceID != identity.RuntimeInstanceID ||
		r.StatusCode != 204 || r.RecordedAt.IsZero() || r.RecordedAt.Before(intentAt) {
		return time.Time{}, fmt.Errorf("offline delete 204 receipt authentication failed")
	}
	return r.RecordedAt, nil
}

func writeOfflineDeleteTerminalOnce(directory string, sealed signedOfflineDeleteTerminal) error {
	data, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, offlineDeleteTerminalName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return syncDirectory(directory)
}
