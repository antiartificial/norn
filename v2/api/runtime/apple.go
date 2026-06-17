package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func (r *Runtime) buildAppleContainer(ctx context.Context, opts BuildOpts) (string, error) {
	args := []string{"build"}
	for k, v := range opts.BuildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, "-f", opts.Dockerfile, "-t", opts.Tag, opts.ContextDir)

	cmd := exec.CommandContext(ctx, "container", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("container build: %s", string(out))
	}

	if r.registryURL != "" && opts.Push {
		registryTag := fmt.Sprintf("%s/%s", r.registryURL, opts.Tag)
		tagCmd := exec.CommandContext(ctx, "container", "image", "tag", opts.Tag, registryTag)
		if tagOut, tagErr := tagCmd.CombinedOutput(); tagErr != nil {
			return "", fmt.Errorf("container image tag: %s", string(tagOut))
		}
		pushCmd := exec.CommandContext(ctx, "container", "image", "push", registryTag)
		if pushOut, pushErr := pushCmd.CombinedOutput(); pushErr != nil {
			return "", fmt.Errorf("container image push: %s", string(pushOut))
		}
		return registryTag, nil
	}

	return opts.Tag, nil
}

func appleContainerAvailable() bool {
	path, err := exec.LookPath("container")
	if err != nil {
		return false
	}
	// Disambiguate from other binaries named "container"
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "container")
}

func appleContainerVersion(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "container", "--version")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func appleContainerCapabilities() []string {
	caps := []string{"build", "run", "vm-isolation"}
	cmd := exec.Command("container", "builder", "status")
	if out, err := cmd.CombinedOutput(); err == nil {
		if strings.Contains(string(out), "running") || strings.Contains(string(out), "Running") {
			caps = append(caps, "builder-active")
		}
	}
	return caps
}
