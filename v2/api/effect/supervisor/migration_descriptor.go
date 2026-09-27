package supervisor

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// MigrationProtocolV1 is reserved for a private migration runner. The
	// generic command runner must not launch this descriptor: it retains raw
	// command output, which can contain database credentials.
	MigrationProtocolV1 = "norn.app-migration-runner/v1"
	MigrationStage      = "app.migrate"
	MaxMigrationTimeout = 24 * time.Hour
)

// MigrationIntent contains only accepted, nonsecret identifiers. Each digest
// is the SHA-256 of the complete corresponding input, calculated before an
// effect is reserved. TargetSHA256 must identify the accepted database target
// without its password; PostconditionSHA256 identifies a reviewed check on
// that original target. Neither a connection URL nor launch environment is
// representable here.
type MigrationIntent struct {
	SourceSHA256        string `json:"sourceSha256"`
	CommandSHA256       string `json:"commandSha256"`
	TargetSHA256        string `json:"targetSha256"`
	TargetBindingID     string `json:"targetBindingId"`
	TargetGeneration    int64  `json:"targetGeneration"`
	PostconditionSHA256 string `json:"postconditionSha256"`
	TimeoutMillis       int64  `json:"timeoutMillis"`
}

// MigrationDescriptor is the durable reservation payload. It cannot launch
// until a dedicated private runner and migration verifier are implemented.
type MigrationDescriptor struct {
	Protocol         string `json:"protocol"`
	SupervisorRootID string `json:"supervisorRootId"`
	Stage            string `json:"stage"`
	MigrationIntent
	IntentMAC string `json:"intentMac"`
}

func (m *Manager) BuildMigrationDescriptor(intent MigrationIntent) (json.RawMessage, error) {
	if m == nil || m.rootID == "" {
		return nil, fmt.Errorf("migration supervisor manager is unavailable")
	}
	if err := validateMigrationIntent(intent); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(MigrationDescriptor{
		Protocol: MigrationProtocolV1, SupervisorRootID: m.rootID, Stage: MigrationStage,
		MigrationIntent: intent, IntentMAC: m.migrationIntentMAC(intent),
	})
	if err != nil {
		return nil, fmt.Errorf("encode migration descriptor: %w", err)
	}
	return encoded, nil
}

func (m *Manager) verifyMigrationDescriptor(payload json.RawMessage) (MigrationDescriptor, error) {
	var descriptor MigrationDescriptor
	if err := decodeStrict(payload, &descriptor); err != nil {
		return MigrationDescriptor{}, fmt.Errorf("decode migration descriptor: %w", err)
	}
	if m == nil || descriptor.Protocol != MigrationProtocolV1 || descriptor.SupervisorRootID != m.rootID ||
		descriptor.Stage != MigrationStage || validateMigrationIntent(descriptor.MigrationIntent) != nil ||
		!validSHA256(descriptor.IntentMAC) || !hmac.Equal([]byte(descriptor.IntentMAC), []byte(m.migrationIntentMAC(descriptor.MigrationIntent))) {
		return MigrationDescriptor{}, fmt.Errorf("unsupported or untrusted migration descriptor")
	}
	return descriptor, nil
}

func (m *Manager) migrationIntentMAC(intent MigrationIntent) string {
	encoded, _ := json.Marshal(struct {
		Protocol, Root, Stage string
		Intent                MigrationIntent
	}{MigrationProtocolV1, m.rootID, MigrationStage, intent})
	return m.mac(encoded)
}

func validateMigrationIntent(intent MigrationIntent) error {
	if !validSHA256(intent.SourceSHA256) || !validSHA256(intent.CommandSHA256) ||
		!validSHA256(intent.TargetSHA256) || !validSHA256(intent.PostconditionSHA256) ||
		!validSnapshotName(intent.TargetBindingID) || intent.TargetGeneration <= 0 ||
		intent.TimeoutMillis <= 0 || intent.TimeoutMillis > MaxMigrationTimeout.Milliseconds() {
		return fmt.Errorf("migration intent requires accepted source, command, target, postcondition and timeout identities")
	}
	return nil
}
