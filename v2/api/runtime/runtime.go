package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Backend string

const (
	Docker         Backend = "docker"
	AppleContainer Backend = "container"
)

type BuildOpts struct {
	ContextDir string
	Dockerfile string
	Tag        string
	BuildArgs  map[string]string
	Platforms  []string
	Push       bool
}

type Info struct {
	Backend      Backend  `json:"backend"`
	Version      string   `json:"version"`
	Available    bool     `json:"available"`
	TaskDriver   string   `json:"taskDriver"`
	BuildCmd     string   `json:"buildCmd"`
	Capabilities []string `json:"capabilities"`
}

type Runtime struct {
	backend     Backend
	registryURL string
}

func New(backend Backend, registryURL string) *Runtime {
	if backend == "" {
		backend = Detect()
	}
	return &Runtime{
		backend:     backend,
		registryURL: registryURL,
	}
}

func Detect() Backend {
	if _, err := exec.LookPath("container"); err == nil {
		if out, err := exec.Command("container", "system", "status").CombinedOutput(); err == nil {
			if strings.Contains(string(out), "running") || strings.Contains(string(out), "Running") {
				return AppleContainer
			}
		}
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return Docker
	}
	return Docker
}

func (r *Runtime) Backend() Backend {
	return r.backend
}

func (r *Runtime) TaskDriver() string {
	switch r.backend {
	case AppleContainer:
		return "docker"
	default:
		return "docker"
	}
}

func (r *Runtime) Build(ctx context.Context, opts BuildOpts) (string, error) {
	switch r.backend {
	case AppleContainer:
		return r.buildAppleContainer(ctx, opts)
	default:
		return r.buildDocker(ctx, opts)
	}
}

func (r *Runtime) Info(ctx context.Context) *Info {
	info := &Info{
		Backend:    r.backend,
		TaskDriver: r.TaskDriver(),
	}
	switch r.backend {
	case AppleContainer:
		info.BuildCmd = "container build"
		info.Available = appleContainerAvailable()
		info.Version = appleContainerVersion(ctx)
		info.Capabilities = appleContainerCapabilities()
	default:
		info.BuildCmd = "docker build"
		info.Available = dockerAvailable()
		info.Version = dockerVersion(ctx)
		info.Capabilities = dockerCapabilities(ctx)
	}
	return info
}

func ParseBackend(s string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "docker", "":
		return Docker, nil
	case "container", "apple", "apple-container":
		return AppleContainer, nil
	case "auto":
		return Detect(), nil
	default:
		return "", fmt.Errorf("unknown container runtime %q (valid: docker, container, auto)", s)
	}
}
