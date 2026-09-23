//go:build linux

package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"norn/v2/api/effect"
)

// NewCgroupBackend requires a delegated cgroup-v2 directory and a verified,
// protocol-compatible runner binary. It has no fallback: any missing piece
// is a startup error.
func NewCgroupBackend(root, runnerBinary, runnerSHA256 string, signingKey []byte) (Backend, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(runnerBinary) == "" || len(signingKey) < 32 {
		return nil, fmt.Errorf("cgroup root, runner binary, and signing key are required")
	}
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("effect cgroup v2 root is unavailable: %w", err)
	}
	if err := VerifyRunnerBinary(runnerBinary, runnerSHA256); err != nil {
		return nil, err
	}
	return newCgroupBackend(root, runnerBinary, signingKey), nil
}

func (b *cgroupBackend) Start(ctx context.Context, execution BackendExecution, material effect.LaunchMaterial) error {
	cgroupPath := b.cgroupPath(execution.RuntimeInstanceID)
	if err := os.Mkdir(cgroupPath, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	// The command runs in a dedicated child cgroup so the runner's timeout
	// kills exactly this execution's command tree, never a reusable PID.
	commandCgroup := filepath.Join(cgroupPath, "command")
	if err := os.Mkdir(commandCgroup, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	cgroup, err := os.Open(cgroupPath)
	if err != nil {
		return err
	}
	defer cgroup.Close()
	request := runnerRequest{
		Protocol: ProtocolV1, Execution: execution, Material: material,
		StatusKey:     hex.EncodeToString(runnerStatusKey(b.key, execution.RuntimeInstanceID)),
		CommandCgroup: commandCgroup,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(execution.StateDirectory, "runner.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		logFile.Close()
		return err
	}
	command := exec.Command(b.runnerBinary)
	command.Stdin = inputReader
	command.Stdout = logFile
	command.Stderr = logFile
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true, UseCgroupFD: true, CgroupFD: int(cgroup.Fd())}
	startErr := command.Start()
	// The child holds its own inherited descriptors. The parent's copies are
	// closed exactly once here, whether or not Start succeeded; the reaper
	// goroutine below never touches them.
	_ = inputReader.Close()
	_ = logFile.Close()
	if startErr != nil {
		_ = inputWriter.Close()
		return startErr
	}
	go func() { _ = command.Wait() }()
	written := make(chan error, 1)
	go func() {
		_, writeErr := inputWriter.Write(encoded)
		closeErr := inputWriter.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		written <- writeErr
	}()
	select {
	case err := <-written:
		if err != nil {
			return fmt.Errorf("send effect runner request: %w", err)
		}
	case <-ctx.Done():
		return fmt.Errorf("effect runner request handoff is ambiguous: %w", ctx.Err())
	}
	handshake := time.NewTicker(10 * time.Millisecond)
	defer handshake.Stop()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for {
		if _, err := readRunnerStatus(execution.StateDirectory, runnerStatusKey(b.key, execution.RuntimeInstanceID), execution.RuntimeInstanceID); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("effect runner startup is ambiguous: %w", ctx.Err())
		case <-timeout.C:
			return fmt.Errorf("effect runner startup handshake timed out")
		case <-handshake.C:
		}
	}
}
