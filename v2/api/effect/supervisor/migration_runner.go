package supervisor

// The migration helper receives its one-shot request over the runner's
// private pipe. It never stores command text, environment values, or command
// output. Its status proves only that the command process ended; a future
// verifier must also establish containment and the original-target database
// postcondition before accepting success.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"norn/v2/api/effect"
)

type migrationRunnerRequest struct {
	Protocol      string              `json:"protocol"`
	Execution     BackendExecution    `json:"execution"`
	Descriptor    MigrationDescriptor `json:"descriptor"`
	Command       string              `json:"command"`
	Directory     string              `json:"directory"`
	Environment   []string            `json:"environment"`
	StatusKey     string              `json:"statusKey"`
	CommandCgroup string              `json:"commandCgroup,omitempty"`
}

type migrationRunnerStatus struct {
	Protocol          string                 `json:"protocol"`
	RuntimeInstanceID string                 `json:"runtimeInstanceId"`
	DescriptorSHA256  string                 `json:"descriptorSha256"`
	Phase             effect.SupervisorPhase `json:"phase"`
	ExitCode          *int                   `json:"exitCode,omitempty"`
	TimedOut          bool                   `json:"timedOut,omitempty"`
	UpdatedAt         time.Time              `json:"updatedAt"`
}

type signedMigrationRunnerStatus struct {
	Status migrationRunnerStatus `json:"status"`
	MAC    string                `json:"mac"`
}

func runMigrationHelper(data []byte) error {
	var request migrationRunnerRequest
	if err := decodeStrict(data, &request); err != nil {
		return fmt.Errorf("migration runner request is malformed")
	}
	key, err := hex.DecodeString(request.StatusKey)
	if request.Protocol != MigrationProtocolV1 || request.Execution.RuntimeInstanceID == "" ||
		!filepath.IsAbs(request.Execution.StateDirectory) || err != nil || len(key) != sha256.Size ||
		request.Descriptor.Protocol != MigrationProtocolV1 || request.Descriptor.Stage != MigrationStage ||
		request.Descriptor.SupervisorRootID == "" || validateMigrationIntent(request.Descriptor.MigrationIntent) != nil ||
		!validSHA256(request.Descriptor.IntentMAC) || !filepath.IsAbs(request.Directory) ||
		request.Command == "" || len(request.Command) > 64<<10 ||
		(request.CommandCgroup != "" && !filepath.IsAbs(request.CommandCgroup)) {
		return fmt.Errorf("migration runner request is incomplete")
	}
	commandSHA := sha256.Sum256([]byte(request.Command))
	if hex.EncodeToString(commandSHA[:]) != request.Descriptor.CommandSHA256 {
		return fmt.Errorf("migration runner command differs from accepted intent")
	}
	if _, err := environmentNames(request.Environment); err != nil {
		return fmt.Errorf("migration runner environment is invalid")
	}
	descriptorBytes, err := json.Marshal(request.Descriptor)
	if err != nil {
		return fmt.Errorf("migration runner descriptor is invalid")
	}
	descriptorSHA := sha256.Sum256(descriptorBytes)
	directory := request.Execution.StateDirectory
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("migration runner state directory is unavailable")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("migration runner state directory is unavailable")
	}
	status := migrationRunnerStatus{Protocol: MigrationProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID,
		DescriptorSHA256: hex.EncodeToString(descriptorSHA[:]), Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC()}
	if err := writeMigrationStatus(directory, key, status); err != nil {
		return err
	}
	var terminator commandTerminator = &processGroupTerminator{}
	if request.CommandCgroup != "" {
		terminator, err = openCgroupTerminator(request.CommandCgroup)
		if err != nil {
			return fmt.Errorf("migration command containment is unavailable")
		}
	}
	material := effect.LaunchMaterial{Argv: []string{"sh", "-c", request.Command}, Directory: request.Directory,
		Environment: request.Environment, Timeout: time.Duration(request.Descriptor.TimeoutMillis) * time.Millisecond}
	timedOut, runErr := runContained(material, io.Discard, terminator, realSchedule, nil)
	status.Phase, status.TimedOut, status.UpdatedAt = effect.SupervisorSucceeded, timedOut, time.Now().UTC()
	exitCode := 0
	if runErr != nil || timedOut {
		status.Phase, exitCode = effect.SupervisorFailed, -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
		}
	}
	status.ExitCode = &exitCode
	return writeMigrationStatus(directory, key, status)
}

func writeMigrationStatus(directory string, key []byte, status migrationRunnerStatus) error {
	encoded, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode migration runner status: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return writeDurableJSON(directory, "migration-status.json", signedMigrationRunnerStatus{Status: status, MAC: hex.EncodeToString(mac.Sum(nil))})
}

func readMigrationStatus(directory string, key []byte, runtimeID string, descriptorSHA string) (migrationRunnerStatus, error) {
	data, err := readBoundedRegular(filepath.Join(directory, "migration-status.json"), maxRunnerStatusBytes)
	if err != nil {
		return migrationRunnerStatus{}, err
	}
	var signed signedMigrationRunnerStatus
	if err := decodeStrict(data, &signed); err != nil {
		return migrationRunnerStatus{}, fmt.Errorf("migration runner status is malformed")
	}
	encoded, err := json.Marshal(signed.Status)
	if err != nil {
		return migrationRunnerStatus{}, fmt.Errorf("migration runner status is malformed")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	if signed.Status.Protocol != MigrationProtocolV1 || signed.Status.RuntimeInstanceID != runtimeID ||
		signed.Status.DescriptorSHA256 != descriptorSHA || signed.Status.UpdatedAt.IsZero() ||
		!hmac.Equal([]byte(signed.MAC), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return migrationRunnerStatus{}, fmt.Errorf("migration runner status is untrusted")
	}
	switch signed.Status.Phase {
	case effect.SupervisorRunning:
		if signed.Status.ExitCode != nil || signed.Status.TimedOut {
			return migrationRunnerStatus{}, fmt.Errorf("migration runner running status is invalid")
		}
	case effect.SupervisorSucceeded, effect.SupervisorFailed:
		if signed.Status.ExitCode == nil || (signed.Status.Phase == effect.SupervisorSucceeded && (*signed.Status.ExitCode != 0 || signed.Status.TimedOut)) {
			return migrationRunnerStatus{}, fmt.Errorf("migration runner terminal status is invalid")
		}
	default:
		return migrationRunnerStatus{}, fmt.Errorf("migration runner status phase is unsupported")
	}
	return signed.Status, nil
}
