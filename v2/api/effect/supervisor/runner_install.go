package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// VerifyRunnerBinary refuses a runner helper that another user could replace,
// that differs from an optional pinned SHA-256, or that does not speak this
// build's protocol. The API calls it once at startup; there is no fallback to
// running build.test commands directly.
func VerifyRunnerBinary(path, expectedSHA256 string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("effect runner binary path must be absolute")
	}
	if err := verifyPrivateExecutable(path); err != nil {
		return err
	}
	if err := verifyPrivateExecutable(filepath.Dir(path)); err != nil {
		return fmt.Errorf("effect runner directory: %w", err)
	}
	if expectedSHA256 != "" {
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open effect runner binary: %w", err)
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		file.Close()
		if copyErr != nil || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expectedSHA256) {
			return fmt.Errorf("effect runner binary does not match its pinned SHA-256")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, ProtocolFlag)
	command.Env = []string{"PATH=/usr/bin:/bin"}
	var output bytes.Buffer
	command.Stdout = &limitedBuffer{buffer: &output, limit: 256}
	if err := command.Run(); err != nil {
		return fmt.Errorf("effect runner protocol handshake failed")
	}
	if strings.TrimSpace(output.String()) != ProtocolV1 {
		return fmt.Errorf("effect runner speaks an incompatible protocol")
	}
	return nil
}

// verifyPrivateExecutable accepts a non-symlink regular file or directory
// owned by the current user or root and not writable by group or others.
func verifyPrivateExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("effect runner is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return fmt.Errorf("effect runner path must not be a symlink or special file")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("effect runner path must not be group or world writable")
	}
	if info.Mode().IsRegular() && info.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("effect runner binary is not executable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0) {
		return fmt.Errorf("effect runner path must be owned by this user or root")
	}
	return nil
}

type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if remaining := b.limit - b.buffer.Len(); remaining > 0 {
		if len(data) > remaining {
			b.buffer.Write(data[:remaining])
		} else {
			b.buffer.Write(data)
		}
	}
	return len(data), nil
}
