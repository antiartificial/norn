package pipeline

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"norn/v2/api/runtime"
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
	if st.sourceDirty {
		sha += "-dirty"
	}
	localTag := fmt.Sprintf("%s:%s", st.spec.App, sha)
	dockerfile := "Dockerfile"
	if st.spec.Build.Dockerfile != "" {
		dockerfile = st.spec.Build.Dockerfile
	}
	dockerfilePath := filepath.Join(st.workDir, dockerfile)

	buildNumber := "0"
	if st.workDir != "" {
		revListCmd := exec.CommandContext(ctx, "git", "-C", st.workDir, "rev-list", "--count", "HEAD")
		if revOut, err := revListCmd.Output(); err == nil {
			buildNumber = strings.TrimSpace(string(revOut))
		}
	}

	push := p.RegistryURL != "" && !st.preflight
	opts := runtime.BuildOpts{
		ContextDir: st.workDir,
		Dockerfile: dockerfilePath,
		Tag:        localTag,
		BuildArgs: map[string]string{
			"VERSION":      st.commitSHA,
			"BUILD_NUMBER": buildNumber,
		},
		Push: push,
	}
	if push {
		opts.Platforms = []string{"linux/amd64", "linux/arm64"}
	}

	tag, err := p.Runtime.Build(ctx, opts)
	if err != nil {
		return err
	}
	st.imageTag = tag
	return nil
}
