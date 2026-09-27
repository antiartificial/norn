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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"norn/v2/api/effect"
)

type migrationRunnerRequest struct {
	Protocol      string                 `json:"protocol"`
	Execution     BackendExecution       `json:"execution"`
	Descriptor    MigrationDescriptor    `json:"descriptor"`
	Command       string                 `json:"command"`
	Directory     string                 `json:"directory"`
	Environment   []string               `json:"environment"`
	PrivateFiles  []migrationPrivateFile `json:"privateFiles,omitempty"`
	StatusKey     string                 `json:"statusKey"`
	CommandCgroup string                 `json:"commandCgroup,omitempty"`
}

// PrivateFiles are sent only on the helper pipe. File names are a closed
// protocol set so future dead-helper recovery can scrub only known names
// without trusting an attacker-controlled cleanup path.
type migrationPrivateFile struct {
	Name     string `json:"name"`
	Contents []byte `json:"contents"`
	Template bool   `json:"template,omitempty"`
}

var migrationPrivateFileNames = []string{"pg_service.conf", "passfile", "connection.url", "sslrootcert.pem", "sslcert.pem", "sslkey.pem"}

const maxMigrationPrivateBytes = 256 << 10

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
	environment, cleanup, err := prepareMigrationPrivateFiles(directory, request.PrivateFiles, request.Environment)
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	var terminator commandTerminator = &processGroupTerminator{}
	if request.CommandCgroup != "" {
		var cgroup *cgroupTerminator
		cgroup, err = openCgroupTerminator(request.CommandCgroup)
		if err != nil {
			return fmt.Errorf("migration command containment is unavailable")
		}
		terminator = &migrationCgroupTerminator{cgroup}
	}
	material := effect.LaunchMaterial{Argv: []string{"sh", "-c", request.Command}, Directory: request.Directory,
		Environment: environment, Timeout: time.Duration(request.Descriptor.TimeoutMillis) * time.Millisecond}
	timedOut, runErr := runContained(material, io.Discard, terminator, realSchedule, nil)
	if err := cleanup(); err != nil {
		return fmt.Errorf("migration runner could not scrub private material")
	}
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

func prepareMigrationPrivateFiles(directory string, files []migrationPrivateFile, environment []string) ([]string, func() error, error) {
	if len(files) > len(migrationPrivateFileNames) {
		return nil, nil, fmt.Errorf("migration runner private file set is invalid")
	}
	allowed := map[string]bool{}
	for _, name := range migrationPrivateFileNames {
		allowed[name] = true
	}
	seen, total := map[string]bool{}, 0
	for _, file := range files {
		if !allowed[file.Name] || seen[file.Name] || len(file.Contents) > 64<<10 {
			return nil, nil, fmt.Errorf("migration runner private file set is invalid")
		}
		seen[file.Name] = true
		total += len(file.Contents)
		if total > maxMigrationPrivateBytes {
			return nil, nil, fmt.Errorf("migration runner private file set is too large")
		}
	}
	private := ""
	paths := map[string]string{}
	if len(files) > 0 {
		var err error
		private, err = os.MkdirTemp(directory, ".migration-")
		if err != nil {
			return nil, nil, fmt.Errorf("migration runner private directory is unavailable")
		}
		if err := os.Chmod(private, 0o700); err != nil {
			_ = os.Remove(private)
			return nil, nil, fmt.Errorf("migration runner private directory is unavailable")
		}
		for _, file := range files {
			paths[file.Name] = filepath.Join(private, file.Name)
		}
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		if private == "" {
			cleaned = true
			return nil
		}
		for _, name := range migrationPrivateFileNames {
			if err := removeSnapshotSecrets(filepath.Join(private, name)); err != nil {
				return err
			}
		}
		if err := os.Remove(private); err != nil {
			return err
		}
		cleaned = true
		return nil
	}
	fail := func() ([]string, func() error, error) {
		_ = cleanup()
		return nil, nil, fmt.Errorf("migration runner private material is invalid")
	}
	for _, file := range files {
		contents := append([]byte(nil), file.Contents...)
		if file.Template {
			contents = []byte(replaceMigrationPrivatePaths(string(contents), paths))
		}
		if containsMigrationPrivateMarker(string(contents)) || writePrivateFile(paths[file.Name], contents) != nil {
			return fail()
		}
	}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			return fail()
		}
		value = replaceMigrationPrivatePaths(value, paths)
		if containsMigrationPrivateMarker(value) || migrationFileEnv(name) && !migrationPrivatePath(value, paths) {
			return fail()
		}
		result = append(result, name+"="+value)
	}
	return result, cleanup, nil
}

func migrationPrivatePath(value string, paths map[string]string) bool {
	for _, path := range paths {
		if value == path {
			return true
		}
	}
	return false
}

func replaceMigrationPrivatePaths(value string, paths map[string]string) string {
	for name, path := range paths {
		value = strings.ReplaceAll(value, "{{private-file:"+name+"}}", path)
		value = strings.ReplaceAll(value, "{{private-file-url:"+name+"}}", url.QueryEscape(path))
	}
	return value
}

func containsMigrationPrivateMarker(value string) bool {
	return strings.Contains(value, "{{private-file:") || strings.Contains(value, "{{private-file-url:")
}

func migrationFileEnv(name string) bool {
	return name == "PGSERVICEFILE" || name == "PGPASSFILE" || name == "PGSSLROOTCERT" ||
		name == "PGSSLCERT" || name == "PGSSLKEY" || strings.HasSuffix(name, "_FILE")
}

// recoverMigrationPrivateMaterial is called only after the backend proves
// the execution cgroup is empty. A signed running status means the helper
// may have died before scrubbing; it is never converted to a terminal result.
func recoverMigrationPrivateMaterial(directory string, key []byte, runtimeID, descriptorSHA string) error {
	status, err := readMigrationStatus(directory, key, runtimeID, descriptorSHA)
	if err != nil {
		return err
	}
	if status.Phase != effect.SupervisorRunning {
		return nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".migration-") {
			continue
		}
		if !entry.IsDir() {
			return fmt.Errorf("migration private material path is not a directory")
		}
		private := filepath.Join(directory, entry.Name())
		for _, name := range migrationPrivateFileNames {
			if err := removeSnapshotSecrets(filepath.Join(private, name)); err != nil {
				return fmt.Errorf("migration private material cannot be scrubbed")
			}
		}
		if err := os.Remove(private); err != nil {
			return fmt.Errorf("migration private material directory cannot be removed")
		}
	}
	return nil
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
