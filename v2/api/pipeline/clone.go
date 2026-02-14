package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"norn/v2/api/saga"
)

func (p *Pipeline) clone(ctx context.Context, st *state, sg *saga.Saga) error {
	workDir, err := os.MkdirTemp("", "norn-build-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	st.workDir = workDir

	if st.spec.Repo != nil {
		args := []string{"clone", "--depth", "1", "--branch", st.spec.Repo.Branch, st.spec.Repo.URL, workDir}
		cmd := exec.CommandContext(ctx, "git", args...)
		gitEnv, cleanup := p.gitEnv(st.spec.Repo.URL)
		if cleanup != nil {
			defer cleanup()
		}
		cmd.Env = append(os.Environ(), gitEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Fall back to local copy
			srcDir := filepath.Join(p.AppsDir, st.spec.App)
			if _, statErr := os.Stat(srcDir); statErr == nil {
				log.Printf("clone: git clone failed (%v), falling back to local copy from %s", err, srcDir)
				cpCmd := exec.CommandContext(ctx, "cp", "-a", srcDir+"/.", workDir)
				if cpOut, cpErr := cpCmd.CombinedOutput(); cpErr != nil {
					return fmt.Errorf("git clone failed and local fallback failed: %s", string(cpOut))
				}
				p.resolveLocalSHA(ctx, srcDir, st)
				return nil
			}
			return fmt.Errorf("git clone: %s", string(out))
		}

		// Resolve HEAD SHA
		revCmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "HEAD")
		if shaOut, revErr := revCmd.Output(); revErr == nil {
			st.commitSHA = strings.TrimSpace(string(shaOut))
		}
		return nil
	}

	// Local copy fallback
	srcDir := filepath.Join(p.AppsDir, st.spec.App)
	cmd := exec.CommandContext(ctx, "cp", "-a", srcDir+"/.", workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy source: %s", string(out))
	}
	p.resolveLocalSHA(ctx, srcDir, st)
	return nil
}

func (p *Pipeline) resolveLocalSHA(ctx context.Context, srcDir string, st *state) {
	revCmd := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "HEAD")
	if shaOut, err := revCmd.Output(); err == nil {
		st.commitSHA = strings.TrimSpace(string(shaOut))
	} else {
		ts := time.Now().Format("20060102150405")
		st.commitSHA = "local-" + ts
	}
}

func (p *Pipeline) gitEnv(url string) (env []string, cleanup func()) {
	if isSSHURL(url) && p.GitSSHKey != "" {
		return []string{
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=accept-new", p.GitSSHKey),
		}, nil
	}
	if !isSSHURL(url) && p.GitToken != "" {
		script, err := os.CreateTemp("", "norn-askpass-*")
		if err != nil {
			return nil, nil
		}
		fmt.Fprintf(script, "#!/bin/sh\necho '%s'\n", p.GitToken)
		script.Close()
		os.Chmod(script.Name(), 0700)
		return []string{
			"GIT_ASKPASS=" + script.Name(),
			"GIT_TERMINAL_PROMPT=0",
		}, func() { os.Remove(script.Name()) }
	}
	return nil, nil
}

func isSSHURL(url string) bool {
	return strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://")
}
