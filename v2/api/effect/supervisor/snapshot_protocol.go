package supervisor

// This file defines the private helper protocol for one PostgreSQL snapshot.
// It deliberately does not expose a general command runner. The durable effect
// descriptor stores only the reviewed, secret-free identity. Credentials and
// service material travel only over the helper's private stdin and are written
// to runner-owned mode-0600 files for the lifetime of pg_dump.

import (
	"bytes"
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
	"strings"
	"syscall"
	"time"

	"norn/v2/api/effect"
)

const (
	// SnapshotProtocolV1 is intentionally separate from the general effect
	// command protocol. A future app.snapshot adapter can use only this shape.
	SnapshotProtocolV1 = "norn.app-snapshot-runner/v1"
	SnapshotStage      = "app.snapshot"
	// MaxSnapshotArtifactBytes is a hard per-artifact ceiling. Production
	// admission must still set a smaller app/database budget before enabling it.
	MaxSnapshotArtifactBytes   int64 = 64 << 30
	maxSnapshotServiceBytes          = 64 << 10
	maxSnapshotPasswordBytes         = 16 << 10
	maxSnapshotDiagnosticBytes       = 1 << 20
)

// Test seams model interruption and hostile local artifacts without changing
// the production protocol surface.
var (
	beforeSnapshotArtifactReservation func(string) error
	beforeSnapshotTerminalStatus      func() error
)

// SnapshotLaunchMaterial is private launch input. ServiceFile and Password
// are secrets and must never be serialized outside the runner request.
type SnapshotLaunchMaterial struct {
	PGDumpPath   string
	PGDumpSHA256 string
	ServiceName  string
	ServiceFile  []byte
	Password     string
	Subject      string
	Timeout      time.Duration
}

// SnapshotDescriptor is safe to store with the durable effect reservation.
// It identifies the pinned pg_dump binary and target identity without storing
// service configuration or a credential.
type SnapshotDescriptor struct {
	Protocol         string `json:"protocol"`
	SupervisorRootID string `json:"supervisorRootId"`
	Stage            string `json:"stage"`
	PGDumpSHA256     string `json:"pgDumpSha256"`
	ServiceName      string `json:"serviceName"`
	Subject          string `json:"subject"`
	TimeoutMillis    int64  `json:"timeoutMillis"`
	MaterialMAC      string `json:"materialMac"`
}

type snapshotRunnerRequest struct {
	Protocol   string                 `json:"protocol"`
	Execution  BackendExecution       `json:"execution"`
	Descriptor SnapshotDescriptor     `json:"descriptor"`
	Material   SnapshotLaunchMaterial `json:"material"`
	StatusKey  string                 `json:"statusKey"`
}

// SnapshotArtifact is an opaque, runner-private artifact record. Regular and
// NoFollow mean the runner opened exactly one non-symlink descriptor, verified
// it was regular, and hashed its complete bounded contents through that fd.
type SnapshotArtifact struct {
	Reference string `json:"reference"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Regular   bool   `json:"regular"`
	NoFollow  bool   `json:"noFollow"`
}

type snapshotRunnerStatus struct {
	Protocol          string                 `json:"protocol"`
	RuntimeInstanceID string                 `json:"runtimeInstanceId"`
	DescriptorSHA256  string                 `json:"descriptorSha256"`
	Phase             effect.SupervisorPhase `json:"phase"`
	ExitCode          *int                   `json:"exitCode,omitempty"`
	Artifact          *SnapshotArtifact      `json:"artifact,omitempty"`
	DiagnosticBytes   int64                  `json:"diagnosticBytes"`
	DiagnosticSHA256  string                 `json:"diagnosticSha256,omitempty"`
	DiagnosticDropped int64                  `json:"diagnosticDropped,omitempty"`
	TimedOut          bool                   `json:"timedOut,omitempty"`
	UpdatedAt         time.Time              `json:"updatedAt"`
}

type signedSnapshotRunnerStatus struct {
	Status snapshotRunnerStatus `json:"status"`
	MAC    string               `json:"mac"`
}

// SnapshotManifest is a bounded signed assertion intended for the future
// snapshot effect verifier. It is issued only after its caller has established
// containment for this helper execution; a runner status alone never claims it.
type SnapshotManifest struct {
	Protocol          string           `json:"protocol"`
	RuntimeInstanceID string           `json:"runtimeInstanceId"`
	DescriptorSHA256  string           `json:"descriptorSha256"`
	Artifact          SnapshotArtifact `json:"artifact"`
	ContainmentProven bool             `json:"containmentProven"`
	ObservedAt        time.Time        `json:"observedAt"`
	MAC               string           `json:"mac"`
}

type snapshotManifestPayload struct {
	Protocol          string           `json:"protocol"`
	RuntimeInstanceID string           `json:"runtimeInstanceId"`
	DescriptorSHA256  string           `json:"descriptorSha256"`
	Artifact          SnapshotArtifact `json:"artifact"`
	ContainmentProven bool             `json:"containmentProven"`
	ObservedAt        time.Time        `json:"observedAt"`
}

func (m *Manager) BuildSnapshotDescriptor(material SnapshotLaunchMaterial) (json.RawMessage, error) {
	if m == nil || m.rootID == "" {
		return nil, fmt.Errorf("effect supervisor manager is unavailable")
	}
	if err := validateSnapshotMaterial(material); err != nil {
		return nil, err
	}
	descriptor := SnapshotDescriptor{Protocol: SnapshotProtocolV1, SupervisorRootID: m.rootID, Stage: SnapshotStage,
		PGDumpSHA256: material.PGDumpSHA256, ServiceName: material.ServiceName, Subject: material.Subject,
		TimeoutMillis: material.Timeout.Milliseconds(), MaterialMAC: m.snapshotMaterialMAC(material)}
	return json.Marshal(descriptor)
}

func (m *Manager) verifySnapshotDescriptor(payload json.RawMessage, material *SnapshotLaunchMaterial) (SnapshotDescriptor, error) {
	var descriptor SnapshotDescriptor
	if err := decodeStrict(payload, &descriptor); err != nil {
		return SnapshotDescriptor{}, fmt.Errorf("decode snapshot runner descriptor: %w", err)
	}
	if descriptor.Protocol != SnapshotProtocolV1 || descriptor.SupervisorRootID != m.rootID || descriptor.Stage != SnapshotStage ||
		descriptor.TimeoutMillis <= 0 || descriptor.TimeoutMillis > MaxSnapshotTimeout.Milliseconds() || !validSHA256(descriptor.PGDumpSHA256) ||
		!validSnapshotName(descriptor.ServiceName) || !validSnapshotSubject(descriptor.Subject) || strings.TrimSpace(descriptor.MaterialMAC) == "" {
		return SnapshotDescriptor{}, fmt.Errorf("unsupported snapshot runner descriptor")
	}
	if material != nil {
		if err := validateSnapshotMaterial(*material); err != nil {
			return SnapshotDescriptor{}, err
		}
		if descriptor.PGDumpSHA256 != material.PGDumpSHA256 || descriptor.ServiceName != material.ServiceName || descriptor.Subject != material.Subject ||
			descriptor.TimeoutMillis != material.Timeout.Milliseconds() || descriptor.MaterialMAC != m.snapshotMaterialMAC(*material) {
			return SnapshotDescriptor{}, fmt.Errorf("snapshot launch material does not match its persisted descriptor")
		}
	}
	return descriptor, nil
}

// MaxSnapshotTimeout bounds one pg_dump helper invocation.
const MaxSnapshotTimeout = time.Hour

func (m *Manager) snapshotMaterialMAC(material SnapshotLaunchMaterial) string {
	encoded, _ := json.Marshal(struct {
		Protocol, Stage, PGDumpPath, PGDumpSHA256, ServiceName, Subject string
		ServiceFile                                                     []byte
		TimeoutMillis                                                   int64
	}{SnapshotProtocolV1, SnapshotStage, material.PGDumpPath, material.PGDumpSHA256, material.ServiceName, material.Subject, material.ServiceFile, material.Timeout.Milliseconds()})
	// Password rotation is intentionally excluded. A later recovery claim must
	// query the original execution and may use a newly issued credential; it
	// must never require the original password to recover the durable effect.
	return m.mac(encoded)
}

func validateSnapshotMaterial(material SnapshotLaunchMaterial) error {
	if !filepath.IsAbs(material.PGDumpPath) || !validSHA256(material.PGDumpSHA256) || !validSnapshotName(material.ServiceName) ||
		!validSnapshotSubject(material.Subject) || material.Timeout <= 0 || material.Timeout > MaxSnapshotTimeout ||
		len(material.ServiceFile) == 0 || len(material.ServiceFile) > maxSnapshotServiceBytes || len(material.Password) == 0 || len(material.Password) > maxSnapshotPasswordBytes || strings.ContainsAny(material.Password, "\x00\r\n") {
		return fmt.Errorf("snapshot launch material is incomplete")
	}
	if !bytes.Contains(material.ServiceFile, []byte("["+material.ServiceName+"]")) || bytes.Contains(bytes.ToLower(material.ServiceFile), []byte("password=")) {
		return fmt.Errorf("snapshot service material is invalid")
	}
	return nil
}

func validSnapshotName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validSnapshotSubject(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n")
}
func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validateSnapshotDescriptorForRunner(descriptor SnapshotDescriptor, material SnapshotLaunchMaterial) error {
	if descriptor.Protocol != SnapshotProtocolV1 || descriptor.Stage != SnapshotStage || descriptor.SupervisorRootID == "" ||
		descriptor.PGDumpSHA256 != material.PGDumpSHA256 || descriptor.ServiceName != material.ServiceName || descriptor.Subject != material.Subject ||
		descriptor.TimeoutMillis != material.Timeout.Milliseconds() || !validSHA256(descriptor.MaterialMAC) {
		return fmt.Errorf("snapshot descriptor does not bind launch material")
	}
	return nil
}

func snapshotDescriptorDigest(descriptor SnapshotDescriptor) (string, error) {
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func runSnapshotHelper(data []byte) error {
	var request snapshotRunnerRequest
	if err := decodeStrict(data, &request); err != nil {
		return fmt.Errorf("snapshot runner request is malformed")
	}
	key, err := hex.DecodeString(request.StatusKey)
	if request.Protocol != SnapshotProtocolV1 || request.Execution.RuntimeInstanceID == "" || !filepath.IsAbs(request.Execution.StateDirectory) || err != nil || len(key) != sha256.Size || validateSnapshotMaterial(request.Material) != nil || validateSnapshotDescriptorForRunner(request.Descriptor, request.Material) != nil {
		return fmt.Errorf("snapshot runner request is incomplete")
	}
	descriptorDigest, err := snapshotDescriptorDigest(request.Descriptor)
	if err != nil {
		return fmt.Errorf("snapshot runner request is incomplete")
	}
	directory := request.Execution.StateDirectory
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("snapshot runner state directory is unavailable")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("snapshot runner state directory is unavailable")
	}
	if err := writeSnapshotStatus(directory, key, snapshotRunnerStatus{Protocol: SnapshotProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID, DescriptorSHA256: descriptorDigest, Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC()}); err != nil {
		return err
	}
	private, err := os.MkdirTemp(directory, ".snapshot-")
	if err != nil {
		return fmt.Errorf("snapshot runner private artifact directory is unavailable")
	}
	if err := os.Chmod(private, 0o700); err != nil {
		return fmt.Errorf("snapshot runner private artifact directory is unavailable")
	}
	servicePath, passwordPath, artifactPath := filepath.Join(private, "service.conf"), filepath.Join(private, "passfile"), filepath.Join(private, "archive.dump")
	secretsPresent := true
	defer func() {
		if secretsPresent {
			_ = removeSnapshotSecrets(servicePath, passwordPath)
		}
	}()
	if err := writePrivateFile(servicePath, request.Material.ServiceFile); err != nil {
		return err
	}
	if err := writePrivateFile(passwordPath, []byte("*:*:*:*:"+escapePGPass(request.Material.Password)+"\n")); err != nil {
		return err
	}
	if beforeSnapshotArtifactReservation != nil {
		if err := beforeSnapshotArtifactReservation(artifactPath); err != nil {
			return err
		}
	}
	// Reserve the exact artifact name first. A pre-existing or swapped foreign
	// artifact causes this attempt to fail before pg_dump is invoked.
	reserved, err := os.OpenFile(artifactPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("snapshot runner artifact reservation failed")
	}
	if err := reserved.Close(); err != nil {
		return err
	}
	diagnostic, err := os.OpenFile(filepath.Join(private, "diagnostic.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("snapshot runner diagnostic file is unavailable")
	}
	capture := &boundedCapture{file: diagnostic, hash: sha256.New(), limit: maxSnapshotDiagnosticBytes}
	timedOut, runErr := runSnapshotCommand(request.Material, artifactPath, servicePath, passwordPath, capture)
	// pg_dump has exited at this point. Scrub its private connection material
	// before inspecting, publishing, or even retaining a failed outcome.
	if err := removeSnapshotSecrets(servicePath, passwordPath); err != nil {
		return fmt.Errorf("snapshot runner could not remove private connection material")
	}
	secretsPresent = false
	syncErr, closeErr := diagnostic.Sync(), diagnostic.Close()
	if capture.err != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("snapshot runner could not durably store diagnostics")
	}
	phase, exitCode := effect.SupervisorSucceeded, 0
	if runErr != nil || timedOut {
		phase, exitCode = effect.SupervisorFailed, -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
		}
	}
	status := snapshotRunnerStatus{Protocol: SnapshotProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID, DescriptorSHA256: descriptorDigest, Phase: phase, ExitCode: &exitCode, DiagnosticBytes: capture.stored, DiagnosticSHA256: hex.EncodeToString(capture.hash.Sum(nil)), DiagnosticDropped: capture.discarded, TimedOut: timedOut, UpdatedAt: time.Now().UTC()}
	if phase == effect.SupervisorSucceeded {
		artifact, err := attestSnapshotArtifact(private, artifactPath, request.Execution.SupervisorExecutionID)
		if err != nil {
			return fmt.Errorf("snapshot runner artifact is not attestable")
		}
		status.Artifact = &artifact
	} else {
		// A failed dump's partial archive is never a candidate snapshot.
		_ = os.Remove(artifactPath)
	}
	if beforeSnapshotTerminalStatus != nil {
		if err := beforeSnapshotTerminalStatus(); err != nil {
			return err
		}
	}
	return writeSnapshotStatus(directory, key, status)
}

func writePrivateFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func removeSnapshotSecrets(paths ...string) error {
	for _, path := range paths {
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("snapshot private connection material is not regular")
		}
		if err == nil {
			if err = file.Truncate(0); err == nil {
				err = file.Sync()
			}
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

// RecoverSnapshotPrivateMaterial removes any private connection files left by
// an interrupted helper. It accepts only an authenticated running status and
// never converts that status into completion or artifact authority.
func RecoverSnapshotPrivateMaterial(directory string, key []byte, runtimeID string) error {
	status, err := readSnapshotStatus(directory, key, runtimeID)
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
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".snapshot-") {
			continue
		}
		private := filepath.Join(directory, entry.Name())
		if err := removeSnapshotSecrets(filepath.Join(private, "service.conf"), filepath.Join(private, "passfile")); err != nil {
			return fmt.Errorf("recover snapshot private connection material: %w", err)
		}
	}
	return nil
}

func escapePGPass(value string) string {
	return strings.NewReplacer("\\", "\\\\", ":", "\\:").Replace(value)
}

func openVerifiedSnapshotPGDump(path, expected string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("snapshot pg_dump binary is unavailable")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		file.Close()
		return nil, fmt.Errorf("snapshot pg_dump binary is invalid")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		file.Close()
		return nil, fmt.Errorf("snapshot pg_dump binary is unreadable")
	}
	if !hmac.Equal([]byte(hex.EncodeToString(hash.Sum(nil))), []byte(expected)) {
		file.Close()
		return nil, fmt.Errorf("snapshot pg_dump binary does not match its pinned digest")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, fmt.Errorf("snapshot pg_dump binary is unreadable")
	}
	return file, nil
}

func verifiedSnapshotCommand(binary *os.File, arguments ...string) *exec.Cmd {
	// Production pg_dump is an ELF/Mach-O executable and is invoked directly.
	// The shell-script path supports deterministic test helpers while keeping
	// their bytes on the same inherited verified descriptor.
	header := make([]byte, 2)
	_, _ = binary.ReadAt(header, 0)
	if bytes.Equal(header, []byte("#!")) {
		return exec.Command("/bin/sh", append([]string{"/dev/fd/3"}, arguments...)...)
	}
	return exec.Command("/dev/fd/3", arguments...)
}

func runSnapshotCommand(material SnapshotLaunchMaterial, artifact, service, password string, capture *boundedCapture) (bool, error) {
	terminator := commandTerminator(&processGroupTerminator{})
	defer terminator.close()
	if !exitObservationSupported {
		return false, fmt.Errorf("snapshot runner cannot observe pg_dump exit safely")
	}
	binary, err := openVerifiedSnapshotPGDump(material.PGDumpPath, material.PGDumpSHA256)
	if err != nil {
		return false, err
	}
	defer binary.Close()
	// ExtraFiles duplicates binary as child fd 3 without close-on-exec. Running
	// /dev/fd/3 executes exactly the regular no-follow descriptor we hashed;
	// replacing material.PGDumpPath after verification cannot change these bytes.
	command := verifiedSnapshotCommand(binary, "-Fc", "--no-owner", "--no-privileges", "--file", artifact, "--dbname=service="+material.ServiceName)
	command.ExtraFiles = []*os.File{binary}
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "PGSERVICEFILE=" + service, "PGPASSFILE=" + password}
	command.Stdout, command.Stderr, command.WaitDelay = capture, capture, outputDrainDelay
	if err := terminator.configure(command); err != nil {
		return false, err
	}
	if err := command.Start(); err != nil {
		return false, err
	}
	terminator.started(command.Process.Pid)
	guard := startTimeoutGuard(material.Timeout, terminator.terminate, realSchedule)
	exitErr := waitExitWithoutReaping(command.Process.Pid)
	timedOut, _ := guard.close()
	err = command.Wait()
	if exitErr != nil && err == nil {
		err = fmt.Errorf("observe pg_dump exit: %w", exitErr)
	}
	return timedOut, err
}

func attestSnapshotArtifact(private, path, executionID string) (SnapshotArtifact, error) {
	if filepath.Dir(path) != private || filepath.Base(path) != "archive.dump" || strings.TrimSpace(executionID) == "" {
		return SnapshotArtifact{}, fmt.Errorf("snapshot artifact path is invalid")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return SnapshotArtifact{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxSnapshotArtifactBytes {
		return SnapshotArtifact{}, fmt.Errorf("snapshot artifact is not a bounded regular file")
	}
	hash := sha256.New()
	copied, err := io.Copy(hash, io.LimitReader(file, MaxSnapshotArtifactBytes+1))
	if err != nil || copied != info.Size() || copied > MaxSnapshotArtifactBytes {
		return SnapshotArtifact{}, fmt.Errorf("snapshot artifact is unreadable")
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() {
		return SnapshotArtifact{}, fmt.Errorf("snapshot artifact changed while being attested")
	}
	return SnapshotArtifact{Reference: "snapshot/" + executionID, Bytes: copied, SHA256: hex.EncodeToString(hash.Sum(nil)), Regular: true, NoFollow: true}, nil
}

func writeSnapshotStatus(directory string, key []byte, status snapshotRunnerStatus) error {
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	return writeDurableJSON(directory, "snapshot-status.json", signedSnapshotRunnerStatus{Status: status, MAC: hex.EncodeToString(mac.Sum(nil))})
}

func readSnapshotStatus(directory string, key []byte, runtimeID string) (snapshotRunnerStatus, error) {
	data, err := readBoundedRegular(filepath.Join(directory, "snapshot-status.json"), maxRunnerStatusBytes)
	if err != nil {
		return snapshotRunnerStatus{}, err
	}
	var envelope signedSnapshotRunnerStatus
	if err := decodeStrict(data, &envelope); err != nil {
		return snapshotRunnerStatus{}, fmt.Errorf("snapshot runner status is malformed")
	}
	encoded, _ := json.Marshal(envelope.Status)
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	status := envelope.Status
	if !hmac.Equal([]byte(envelope.MAC), []byte(hex.EncodeToString(mac.Sum(nil)))) || status.Protocol != SnapshotProtocolV1 || status.RuntimeInstanceID != runtimeID || !validSHA256(status.DescriptorSHA256) {
		return snapshotRunnerStatus{}, fmt.Errorf("snapshot runner status authentication failed")
	}
	if status.Phase == effect.SupervisorRunning {
		// A running record is deliberately readable for recovery, but it has no
		// artifact and can never become a manifest until a later terminal status
		// has been authenticated and containment is independently proven.
		return status, nil
	}
	if status.ExitCode == nil || status.DiagnosticBytes < 0 || !validSHA256(status.DiagnosticSHA256) {
		return snapshotRunnerStatus{}, fmt.Errorf("snapshot runner terminal status is incomplete")
	}
	if status.Phase == effect.SupervisorSucceeded {
		if status.Artifact == nil || !status.Artifact.Regular || !status.Artifact.NoFollow || status.Artifact.Bytes <= 0 || status.Artifact.Bytes > MaxSnapshotArtifactBytes || !validSHA256(status.Artifact.SHA256) || status.Artifact.Reference == "" {
			return snapshotRunnerStatus{}, fmt.Errorf("snapshot runner status has no valid artifact")
		}
	} else if status.Phase != effect.SupervisorFailed {
		return snapshotRunnerStatus{}, fmt.Errorf("snapshot runner status phase is unsupported")
	}
	return status, nil
}

// ReadSnapshotManifest verifies the signed terminal status and the artifact
// again through a no-follow descriptor. The caller must pass the cgroup's
// current result; callers cannot turn an empty or unknown state into proof.
func ReadSnapshotManifest(directory string, key []byte, runtimeID string, contained bool) (SnapshotManifest, error) {
	if !contained {
		return SnapshotManifest{}, fmt.Errorf("snapshot containment is not proven")
	}
	status, err := readSnapshotStatus(directory, key, runtimeID)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if status.Phase != effect.SupervisorSucceeded || status.Artifact == nil {
		return SnapshotManifest{}, fmt.Errorf("snapshot has no successful terminal artifact")
	}
	// The private directory name is intentionally not persisted in the status;
	// discover only a single runner-created .snapshot-* directory and reject
	// ambiguity. This remains private state, not a public artifact path.
	entries, err := os.ReadDir(directory)
	if err != nil {
		return SnapshotManifest{}, err
	}
	var candidate string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".snapshot-") {
			if candidate != "" {
				return SnapshotManifest{}, fmt.Errorf("snapshot artifact directory is ambiguous")
			}
			candidate = filepath.Join(directory, entry.Name())
		}
	}
	if candidate == "" {
		return SnapshotManifest{}, fmt.Errorf("snapshot artifact directory is missing")
	}
	privateEntries, err := os.ReadDir(candidate)
	if err != nil {
		return SnapshotManifest{}, err
	}
	for _, entry := range privateEntries {
		if entry.Name() == "service.conf" || entry.Name() == "passfile" {
			return SnapshotManifest{}, fmt.Errorf("snapshot private connection material remains")
		}
	}
	artifact, err := attestSnapshotArtifact(candidate, filepath.Join(candidate, "archive.dump"), strings.TrimPrefix(status.Artifact.Reference, "snapshot/"))
	if err != nil {
		return SnapshotManifest{}, err
	}
	if artifact != *status.Artifact {
		return SnapshotManifest{}, fmt.Errorf("snapshot artifact does not match signed status")
	}
	manifest := SnapshotManifest{Protocol: SnapshotProtocolV1, RuntimeInstanceID: runtimeID, DescriptorSHA256: status.DescriptorSHA256, Artifact: artifact, ContainmentProven: true, ObservedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(snapshotManifestPayload{manifest.Protocol, manifest.RuntimeInstanceID, manifest.DescriptorSHA256, manifest.Artifact, manifest.ContainmentProven, manifest.ObservedAt})
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	manifest.MAC = hex.EncodeToString(mac.Sum(nil))
	return manifest, nil
}

// VerifySnapshotManifest authenticates the portable attestation itself. The
// verifier must still retrieve the artifact by the private runner reference
// and re-check its bytes before publication or restore.
func VerifySnapshotManifest(manifest SnapshotManifest, key []byte, runtimeID string) error {
	if manifest.Protocol != SnapshotProtocolV1 || manifest.RuntimeInstanceID != runtimeID || !validSHA256(manifest.DescriptorSHA256) || !manifest.ContainmentProven || manifest.ObservedAt.IsZero() ||
		manifest.Artifact.Reference == "" || manifest.Artifact.Bytes <= 0 || manifest.Artifact.Bytes > MaxSnapshotArtifactBytes || !manifest.Artifact.Regular || !manifest.Artifact.NoFollow || !validSHA256(manifest.Artifact.SHA256) {
		return fmt.Errorf("snapshot manifest is incomplete")
	}
	encoded, _ := json.Marshal(snapshotManifestPayload{manifest.Protocol, manifest.RuntimeInstanceID, manifest.DescriptorSHA256, manifest.Artifact, manifest.ContainmentProven, manifest.ObservedAt})
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	if !hmac.Equal([]byte(manifest.MAC), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return fmt.Errorf("snapshot manifest authentication failed")
	}
	return nil
}

// VerifySnapshotManifestForDescriptor rejects a valid artifact assertion from
// another app/database target. Descriptor material contains no password, so a
// credential rotation does not change this stable binding.
func VerifySnapshotManifestForDescriptor(manifest SnapshotManifest, descriptor SnapshotDescriptor, key []byte, runtimeID string) error {
	if err := VerifySnapshotManifest(manifest, key, runtimeID); err != nil {
		return err
	}
	digest, err := snapshotDescriptorDigest(descriptor)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(manifest.DescriptorSHA256), []byte(digest)) {
		return fmt.Errorf("snapshot manifest is bound to another descriptor")
	}
	return nil
}

// CopySnapshotArtifact re-verifies the signed descriptor-bound manifest before
// streaming the private node-local artifact. Callers receive no filesystem
// path, so publication must explicitly copy verified bytes into its namespace.
func CopySnapshotArtifact(directory string, key []byte, runtimeID string, descriptor SnapshotDescriptor, contained bool, destination io.Writer) (SnapshotManifest, error) {
	manifest, err := ReadSnapshotManifest(directory, key, runtimeID, contained)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if err := VerifySnapshotManifestForDescriptor(manifest, descriptor, key, runtimeID); err != nil {
		return SnapshotManifest{}, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return SnapshotManifest{}, err
	}
	var private string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".snapshot-") {
			if private != "" {
				return SnapshotManifest{}, fmt.Errorf("snapshot artifact directory is ambiguous")
			}
			private = filepath.Join(directory, e.Name())
		}
	}
	if private == "" {
		return SnapshotManifest{}, fmt.Errorf("snapshot artifact directory is missing")
	}
	file, err := os.OpenFile(filepath.Join(private, "archive.dump"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return SnapshotManifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Artifact.Bytes {
		return SnapshotManifest{}, fmt.Errorf("snapshot artifact changed before copy")
	}
	hash := sha256.New()
	copied, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(file, MaxSnapshotArtifactBytes+1))
	if err != nil || copied != manifest.Artifact.Bytes || hex.EncodeToString(hash.Sum(nil)) != manifest.Artifact.SHA256 {
		return SnapshotManifest{}, fmt.Errorf("snapshot artifact changed during copy")
	}
	return manifest, nil
}
