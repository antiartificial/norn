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
				st.sourceKind = "local_fallback"
				st.sourcePath = srcDir
				p.recordSourceProvenance(ctx, st, sg)
				return nil
			}
			return fmt.Errorf("git clone: %s", string(out))
		}
		if err := p.checkoutRequestedRef(ctx, workDir, st.sourceRef, st.spec.Repo.URL); err != nil {
			return err
		}

		// Resolve HEAD SHA
		revCmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "HEAD")
		if shaOut, revErr := revCmd.Output(); revErr == nil {
			st.commitSHA = strings.TrimSpace(string(shaOut))
		}
		st.sourceKind = "git_clone"
		st.sourcePath = st.spec.Repo.URL
		p.recordSourceProvenance(ctx, st, sg)
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
	st.sourceKind = "local_copy"
	st.sourcePath = srcDir
	p.recordSourceProvenance(ctx, st, sg)
	return nil
}

func (p *Pipeline) checkoutRequestedRef(ctx context.Context, workDir, ref, repoURL string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		return nil
	}
	fetchCmd := exec.CommandContext(ctx, "git", "-C", workDir, "fetch", "--depth", "1", "origin", ref)
	gitEnv, cleanup := p.gitEnv(repoURL)
	if cleanup != nil {
		defer cleanup()
	}
	fetchCmd.Env = append(os.Environ(), gitEnv...)
	if out, err := fetchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch ref %s: %s", ref, string(out))
	}
	checkoutCmd := exec.CommandContext(ctx, "git", "-C", workDir, "checkout", "--detach", "FETCH_HEAD")
	if out, err := checkoutCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout ref %s: %s", ref, string(out))
	}
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
	st.sourceDirty, st.sourceChanges = localGitChanges(ctx, srcDir)
}

func (p *Pipeline) gitEnv(url string) (env []string, cleanup func()) {
	if isSSHURL(url) && p.GitSSHKey != "" {
		return []string{
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s", p.GitSSHKey, filepath.Join(p.AppsDir, "..", "known_hosts")),
		}, nil
	}
	if !isSSHURL(url) && p.GitToken != "" {
		if strings.ContainsAny(p.GitToken, "\r\n") {
			return nil, nil
		}
		script, err := os.CreateTemp("", "norn-askpass-*")
		if err != nil {
			return nil, nil
		}
		cleanupPath := script.Name()
		tokenFile, err := os.CreateTemp("", "norn-git-token-*")
		if err != nil {
			_ = script.Close()
			_ = os.Remove(cleanupPath)
			return nil, nil
		}
		tokenPath := tokenFile.Name()
		cleanup := func() {
			_ = os.Remove(cleanupPath)
			_ = os.Remove(tokenPath)
		}
		if _, err := tokenFile.WriteString(p.GitToken); err != nil {
			_ = tokenFile.Close()
			_ = script.Close()
			cleanup()
			return nil, nil
		}
		if err := tokenFile.Close(); err != nil {
			_ = script.Close()
			cleanup()
			return nil, nil
		}
		if err := os.Chmod(tokenPath, 0o600); err != nil {
			_ = script.Close()
			cleanup()
			return nil, nil
		}
		if _, err := script.WriteString("#!/bin/sh\nIFS= read -r token < \"$NORN_GIT_ASKPASS_TOKEN_FILE\"\nprintf '%s\\n' \"$token\"\n"); err != nil {
			_ = script.Close()
			cleanup()
			return nil, nil
		}
		if err := script.Close(); err != nil {
			cleanup()
			return nil, nil
		}
		if err := os.Chmod(cleanupPath, 0o700); err != nil {
			cleanup()
			return nil, nil
		}
		return []string{
			"GIT_ASKPASS=" + cleanupPath,
			"GIT_ASKPASS_REQUIRE=force",
			"GIT_TERMINAL_PROMPT=0",
			"NORN_GIT_ASKPASS_TOKEN_FILE=" + tokenPath,
		}, cleanup
	}
	return nil, nil
}

func isSSHURL(url string) bool {
	return strings.HasPrefix(url, "git@") || strings.HasPrefix(url, "ssh://")
}
