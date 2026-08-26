package runtime

import (
	"context"
	"fmt"
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
		backend = Docker
	}
	return &Runtime{
		backend:     backend,
		registryURL: registryURL,
	}
}

func (r *Runtime) Backend() Backend {
	return r.backend
}

func (r *Runtime) TaskDriver() string {
	switch r.backend {
	case AppleContainer:
		return "apple-container"
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
	if ctx == nil {
		ctx = context.Background()
	}
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
		return "", fmt.Errorf("automatic runtime selection is disabled; choose docker or apple-container explicitly")
	default:
		return "", fmt.Errorf("unknown container runtime %q (valid: docker, apple-container)", s)
	}
}
