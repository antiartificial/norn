package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func (r *Runtime) buildDocker(ctx context.Context, opts BuildOpts) (string, error) {
	if r.registryURL != "" && opts.Push {
		registryTag := fmt.Sprintf("%s/%s", r.registryURL, opts.Tag)
		args := []string{"buildx", "build"}
		if len(opts.Platforms) > 0 {
			args = append(args, "--platform", strings.Join(opts.Platforms, ","))
		}
		for k, v := range opts.BuildArgs {
			args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
		}
		args = append(args, "-f", opts.Dockerfile, "-t", registryTag, "--push", opts.ContextDir)

		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker buildx build: %s", string(out))
		}
		return registryTag, nil
	}

	args := []string{"build"}
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, "-f", opts.Dockerfile, "-t", opts.Tag, opts.ContextDir)

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker build: %s", string(out))
	}
	return opts.Tag, nil
}

func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func dockerVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	out, err := cmd.Output()
	if err != nil {
		cmd = exec.CommandContext(ctx, "docker", "--version")
		out, err = cmd.Output()
		if err != nil {
			return "unknown"
		}
	}
	return strings.TrimSpace(string(out))
}

func dockerCapabilities(ctx context.Context) []string {
	caps := []string{"build", "run"}
	cmd := exec.CommandContext(ctx, "docker", "buildx", "version")
	if err := cmd.Run(); err == nil {
		caps = append(caps, "buildx", "multi-arch")
	}
	return caps
}
