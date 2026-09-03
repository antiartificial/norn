package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"norn/v2/api/model"
	containerruntime "norn/v2/api/runtime"
	"norn/v2/api/saga"
)

func (p *Pipeline) build(ctx context.Context, st *state, sg *saga.Saga) error {
	// Release lanes adopt a CI-published immutable digest. Rebuilding here
	// would sever the attestation subject from the deployed artifact.
	if st.artifactBound {
		return nil
	}
	if st.spec.Build == nil {
		st.imageTag = fmt.Sprintf("%s:latest", st.spec.App)
		return nil
	}
	if image := strings.TrimSpace(st.spec.Build.Image); image != "" {
		if p.Production && !model.IsContentAddressedImage(image) {
			return fmt.Errorf("production prebuilt image must be pinned by sha256 OCI digest")
		}
		st.imageTag = image
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

	// Build number from git commit count
	buildNumber := "0"
	if st.workDir != "" {
		revListCmd := exec.CommandContext(ctx, "git", "-C", st.workDir, "rev-list", "--count", "HEAD")
		if revOut, err := revListCmd.Output(); err == nil {
			buildNumber = strings.TrimSpace(string(revOut))
		}
	}

	if p.RegistryURL != "" && !st.preflight {
		if p.ContainerRuntime != nil && p.ContainerRuntime.Backend() == containerruntime.AppleContainer {
			if p.Production {
				return fmt.Errorf("apple-container builds are development-only until provenance, SBOM, signing, and multi-architecture registry receipts are verified")
			}
			image, err := p.ContainerRuntime.Build(ctx, containerruntime.BuildOpts{
				ContextDir: st.workDir, Dockerfile: dockerfilePath, Tag: localTag,
				BuildArgs: map[string]string{"VERSION": st.commitSHA, "BUILD_NUMBER": buildNumber},
				Push:      true,
			})
			if err != nil {
				return err
			}
			st.imageTag = image
			return nil
		}
		registryTag := fmt.Sprintf("%s/%s", p.RegistryURL, localTag)
		registryRepository := fmt.Sprintf("%s/%s", p.RegistryURL, st.spec.App)
		args := []string{
			"buildx", "build",
			"--platform", "linux/amd64,linux/arm64",
			"--build-arg", fmt.Sprintf("VERSION=%s", st.commitSHA),
			"--build-arg", fmt.Sprintf("BUILD_NUMBER=%s", buildNumber),
			"-f", dockerfilePath,
			"-t", registryTag,
			"--push",
		}
		metadataPath := ""
		if p.Production {
			args = append(args, "--provenance=mode=max", "--sbom=true")
			metadata, err := os.CreateTemp("", "norn-build-metadata-*.json")
			if err != nil {
				return fmt.Errorf("create build metadata file: %w", err)
			}
			metadataPath = metadata.Name()
			if err := metadata.Close(); err != nil {
				_ = os.Remove(metadataPath)
				return fmt.Errorf("close build metadata file: %w", err)
			}
			defer os.Remove(metadataPath)
			args = append(args, "--metadata-file", metadataPath)
		}
		args = append(args, st.workDir)
		// Use buildx to build multi-arch and push in one step
		cmd := exec.CommandContext(ctx, "docker", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker build: %s", string(out))
		}
		if p.Production {
			imageRef, err := imageReferenceFromMetadata(registryRepository, metadataPath)
			if err != nil {
				return fmt.Errorf("production image digest: %w", err)
			}
			st.imageTag = imageRef
		} else {
			st.imageTag = registryTag
		}
	} else {
		if p.ContainerRuntime != nil && p.ContainerRuntime.Backend() == containerruntime.AppleContainer {
			image, err := p.ContainerRuntime.Build(ctx, containerruntime.BuildOpts{
				ContextDir: st.workDir, Dockerfile: dockerfilePath, Tag: localTag,
				BuildArgs: map[string]string{"VERSION": st.commitSHA, "BUILD_NUMBER": buildNumber},
			})
			if err != nil {
				return err
			}
			st.imageTag = image
			return nil
		}
		cmd := exec.CommandContext(ctx, "docker", "build",
			"--build-arg", fmt.Sprintf("VERSION=%s", st.commitSHA),
			"--build-arg", fmt.Sprintf("BUILD_NUMBER=%s", buildNumber),
			"-f", dockerfilePath,
			"-t", localTag, st.workDir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker build: %s", string(out))
		}
		st.imageTag = localTag
	}

	return nil
}

func imageReferenceFromMetadata(repository, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var metadata map[string]any
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", fmt.Errorf("parse buildx metadata: %w", err)
	}
	digest, _ := metadata["containerimage.digest"].(string)
	if digest == "" {
		if descriptor, ok := metadata["containerimage.descriptor"].(map[string]any); ok {
			digest, _ = descriptor["digest"].(string)
		}
	}
	ref := strings.TrimRight(strings.TrimSpace(repository), "/") + "@" + strings.TrimSpace(digest)
	if !model.IsContentAddressedImage(ref) {
		return "", fmt.Errorf("buildx did not report a valid sha256 image digest")
	}
	return ref, nil
}
