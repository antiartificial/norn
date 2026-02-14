package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"norn/v2/api/saga"
)

func (p *Pipeline) build(ctx context.Context, st *state, sg *saga.Saga) error {
	if st.spec.Build == nil {
		st.imageTag = fmt.Sprintf("%s:latest", st.spec.App)
		return nil
	}

	sha := st.commitSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	localTag := fmt.Sprintf("%s:%s", st.spec.App, sha)

	// Build number from git commit count
	buildNumber := "0"
	if st.workDir != "" {
		revListCmd := exec.CommandContext(ctx, "git", "-C", st.workDir, "rev-list", "--count", "HEAD")
		if revOut, err := revListCmd.Output(); err == nil {
			buildNumber = strings.TrimSpace(string(revOut))
		}
	}

	if p.RegistryURL != "" {
		registryTag := fmt.Sprintf("%s/%s", p.RegistryURL, localTag)
		// Use buildx to build multi-arch and push in one step
		cmd := exec.CommandContext(ctx, "docker", "buildx", "build",
			"--platform", "linux/amd64,linux/arm64",
			"--build-arg", fmt.Sprintf("VERSION=%s", st.commitSHA),
			"--build-arg", fmt.Sprintf("BUILD_NUMBER=%s", buildNumber),
			"-t", registryTag,
			"--push",
			st.workDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker build: %s", string(out))
		}
		st.imageTag = registryTag
	} else {
		cmd := exec.CommandContext(ctx, "docker", "build",
			"--build-arg", fmt.Sprintf("VERSION=%s", st.commitSHA),
			"--build-arg", fmt.Sprintf("BUILD_NUMBER=%s", buildNumber),
			"-t", localTag, st.workDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker build: %s", string(out))
		}
		st.imageTag = localTag
	}

	return nil
}
