package supervisor

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"norn/v2/api/effect"
)

const (
	// MaxResultBytes bounds stored command output. Further output is drained
	// and counted so the command never blocks on a full pipe, but not stored.
	MaxResultBytes        = 16 << 20
	maxRunnerRequestBytes = 1 << 20
	maxRunnerStatusBytes  = 64 << 10
	// ProtocolFlag makes the runner print its protocol and exit, so the API
	// can refuse an incompatible installed helper before accepting work.
	ProtocolFlag = "--protocol"
	// outputDrainDelay bounds how long the runner waits for output after the
	// command exits. Descendants still holding the pipe keep the cgroup
	// populated, so the terminal status is still not accepted until they exit.
	outputDrainDelay = 10 * time.Second
)

type runnerRequest struct {
	Protocol  string                `json:"protocol"`
	Execution BackendExecution      `json:"execution"`
	Material  effect.LaunchMaterial `json:"material"`
	StatusKey string                `json:"statusKey"`
	// CommandCgroup is a dedicated child cgroup for the command, created by
	// the backend. When set, the timeout kills it through a directory
	// descriptor; otherwise the command's process group is used.
	CommandCgroup string `json:"commandCgroup,omitempty"`
}

// syncResult is the result-file durability step; tests replace it to prove
// that a sync failure withholds the terminal status.
var syncResult = func(file *os.File) error { return file.Sync() }

// runnerStatus is authenticated with a per-runtime key. A terminal status
// binds the exact stored result length and digest, and is published only
// after the result file is fsynced and closed.
type runnerStatus struct {
	Protocol          string                 `json:"protocol"`
	RuntimeInstanceID string                 `json:"runtimeInstanceId"`
	Phase             effect.SupervisorPhase `json:"phase"`
	ExitCode          *int                   `json:"exitCode,omitempty"`
	ResultBytes       int64                  `json:"resultBytes"`
	ResultSHA256      string                 `json:"resultSha256,omitempty"`
	OutputDiscarded   int64                  `json:"outputDiscarded,omitempty"`
	TimedOut          bool                   `json:"timedOut,omitempty"`
	UpdatedAt         time.Time              `json:"updatedAt"`
}

type signedRunnerStatus struct {
	Status runnerStatus `json:"status"`
	MAC    string       `json:"mac"`
}

func (s runnerStatus) terminal() bool {
	return s.Phase == effect.SupervisorSucceeded || s.Phase == effect.SupervisorFailed
}

// RunHelper executes one already-contained request received on a private pipe.
// The parent backend must place this helper in its cgroup before exec; the
// helper deliberately does not claim that the cgroup is empty while it is
// still a member.
func RunHelper(input io.Reader) error {
	request, err := decodeRunnerRequest(input)
	if err != nil {
		return err
	}
	key, _ := hex.DecodeString(request.StatusKey)
	directory := request.Execution.StateDirectory
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("effect runner state directory is unavailable")
	}
	if err := writeRunnerStatus(directory, key, runnerStatus{
		Protocol: ProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID,
		Phase: effect.SupervisorRunning, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	var terminator commandTerminator = &processGroupTerminator{}
	if request.CommandCgroup != "" {
		cgroup, err := openCgroupTerminator(request.CommandCgroup)
		if err != nil {
			return err
		}
		terminator = cgroup
	}
	output, err := os.OpenFile(filepath.Join(directory, "result.bin"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		_ = terminator.close()
		return fmt.Errorf("effect runner result file is unavailable")
	}
	capture := &boundedCapture{file: output, hash: sha256.New(), limit: MaxResultBytes}
	timedOut, runErr := runContained(request.Material, capture, terminator, realSchedule, nil)
	// Publication order: all output is stored, fsynced and closed before the
	// signed terminal status can name its length and digest.
	syncErr := syncResult(output)
	closeErr := output.Close()
	if capture.err != nil || syncErr != nil || closeErr != nil {
		// Leave the status "running": an empty cgroup without a terminal status
		// is observed as unknown, never as success or failure.
		return fmt.Errorf("effect runner could not durably store command output")
	}
	phase := effect.SupervisorSucceeded
	exitCode := 0
	if runErr != nil || timedOut {
		phase = effect.SupervisorFailed
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
		}
	}
	return writeRunnerStatus(directory, key, runnerStatus{
		Protocol: ProtocolV1, RuntimeInstanceID: request.Execution.RuntimeInstanceID,
		Phase: phase, ExitCode: &exitCode, ResultBytes: capture.stored, ResultSHA256: hex.EncodeToString(capture.hash.Sum(nil)),
		OutputDiscarded: capture.discarded, TimedOut: timedOut, UpdatedAt: time.Now().UTC(),
	})
}

// boundedCapture stores at most limit bytes, hashing exactly what it stores.
// It never returns a write error to the command, so a full disk cannot
// SIGPIPE the command into a misleading outcome; the error is reported after.
type boundedCapture struct {
	file      *os.File
	hash      hash.Hash
	limit     int64
	stored    int64
	discarded int64
	err       error
}

func (c *boundedCapture) Write(data []byte) (int, error) {
	keep := int64(len(data))
	if remaining := c.limit - c.stored; keep > remaining {
		keep = remaining
	}
	if keep > 0 && c.err == nil {
		written, err := c.file.Write(data[:keep])
		c.hash.Write(data[:written])
		c.stored += int64(written)
		if err != nil {
			c.err = err
		}
	}
	c.discarded += int64(len(data)) - keep
	return len(data), nil
}

// decodeRunnerRequest reads one bounded JSON object and nothing else. Errors
// never echo request content, which contains command text and environment.
func decodeRunnerRequest(input io.Reader) (runnerRequest, error) {
	malformed := fmt.Errorf("effect runner request is malformed")
	data, err := io.ReadAll(io.LimitReader(input, maxRunnerRequestBytes+1))
	if err != nil || len(data) > maxRunnerRequestBytes {
		return runnerRequest{}, malformed
	}
	var request runnerRequest
	if err := decodeStrict(data, &request); err != nil {
		return runnerRequest{}, malformed
	}
	key, err := hex.DecodeString(request.StatusKey)
	if request.Protocol != ProtocolV1 || request.Execution.RuntimeInstanceID == "" || !filepath.IsAbs(request.Execution.StateDirectory) ||
		err != nil || len(key) != sha256.Size {
		return runnerRequest{}, fmt.Errorf("effect runner request is incomplete")
	}
	if request.CommandCgroup != "" && !filepath.IsAbs(request.CommandCgroup) {
		return runnerRequest{}, fmt.Errorf("effect runner request is incomplete")
	}
	if err := validateBuildTestMaterial(request.Material); err != nil {
		return runnerRequest{}, err
	}
	return request, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func writeRunnerStatus(directory string, key []byte, status runnerStatus) error {
	encoded, err := json.Marshal(status)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	return writeDurableJSON(directory, "runner-status.json", signedRunnerStatus{Status: status, MAC: hex.EncodeToString(mac.Sum(nil))})
}

func readRunnerStatus(directory string, key []byte, runtimeID string) (runnerStatus, error) {
	data, err := readBoundedRegular(filepath.Join(directory, "runner-status.json"), maxRunnerStatusBytes)
	if err != nil {
		return runnerStatus{}, err
	}
	var envelope signedRunnerStatus
	if err := decodeStrict(data, &envelope); err != nil {
		return runnerStatus{}, fmt.Errorf("effect runner status is malformed")
	}
	encoded, _ := json.Marshal(envelope.Status)
	mac := hmac.New(sha256.New, key)
	mac.Write(encoded)
	status := envelope.Status
	if !hmac.Equal([]byte(envelope.MAC), []byte(hex.EncodeToString(mac.Sum(nil)))) ||
		status.Protocol != ProtocolV1 || status.RuntimeInstanceID != runtimeID {
		return runnerStatus{}, fmt.Errorf("effect runner status authentication failed")
	}
	switch status.Phase {
	case effect.SupervisorRunning:
	case effect.SupervisorSucceeded, effect.SupervisorFailed:
		digest, err := hex.DecodeString(status.ResultSHA256)
		if status.ExitCode == nil || err != nil || len(digest) != sha256.Size || status.ResultBytes < 0 || status.ResultBytes > MaxResultBytes {
			return runnerStatus{}, fmt.Errorf("effect runner terminal status is incomplete")
		}
	default:
		return runnerStatus{}, fmt.Errorf("effect runner status phase is unsupported")
	}
	return status, nil
}

// readVerifiedResult reads result.bin through one non-following descriptor and
// accepts it only if it is exactly the length and digest the authenticated
// terminal status recorded. Replacement, truncation or growth after the
// status was published are all rejected.
func readVerifiedResult(directory string, status runnerStatus) ([]byte, error) {
	if !status.terminal() {
		return nil, fmt.Errorf("effect result has no terminal status")
	}
	data, err := readBoundedRegular(filepath.Join(directory, "result.bin"), MaxResultBytes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if int64(len(data)) != status.ResultBytes || hex.EncodeToString(digest[:]) != status.ResultSHA256 {
		return nil, fmt.Errorf("effect result does not match its signed terminal status")
	}
	return data, nil
}

func readBoundedRegular(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("effect supervisor file is not a regular file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("effect supervisor file exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("effect supervisor file exceeds %d bytes", limit)
	}
	return data, nil
}

func runnerStatusKey(master []byte, runtimeID string) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("norn-effect-runner-status-v1\x00"))
	mac.Write([]byte(runtimeID))
	return mac.Sum(nil)
}
