package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

func (r *Runtime) buildAppleContainer(ctx context.Context, opts BuildOpts) (string, error) {
	args := []string{"build"}
	keys := make([]string, 0, len(opts.BuildArgs))
	for key := range opts.BuildArgs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := opts.BuildArgs[k]
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
	out, err := exec.Command(path, "system", "status", "--format", "json").CombinedOutput()
	if err != nil {
		return false
	}
	status := strings.ToLower(string(out))
	if strings.Contains(status, "not running") || strings.Contains(status, "stopped") || strings.Contains(status, "unregistered") {
		return false
	}
	return strings.Contains(status, "running") || strings.Contains(status, "ready")
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
	return []string{"build", "run", "vm-isolation", "user-defined-networks", "port-publish", "stats", "exec"}
}
