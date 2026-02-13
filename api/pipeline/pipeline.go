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

	ncron "norn/api/cron"
	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/store"
)

type Pipeline struct {
	DB          *store.DB
	Kube        *k8s.Client
	WS          *hub.Hub
	Scheduler   *ncron.Scheduler
	AppsDir     string
	GitToken    string
	GitSSHKey   string
	RegistryURL string
}

func (p *Pipeline) Run(deploy *model.Deployment, spec *model.InfraSpec) {
	ctx := context.Background()
	steps := []step{
		{name: "clone", fn: p.clone},
		{name: "build", fn: p.build},
		{name: "test", fn: p.test},
		{name: "snapshot", fn: p.snapshot},
		{name: "migrate", fn: p.migrate},
		{name: "deploy", fn: p.deploy},
		{name: "cleanup", fn: p.cleanup},
	}

	for _, s := range steps {
		deploy.Status = statusForStep(s.name)
		p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status, deploy.Steps, "")
		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: deploy.App, Payload: map[string]string{
			"step":   s.name,
			"status": string(deploy.Status),
		}})

		start := time.Now()
		output, err := s.fn(ctx, deploy, spec)
		elapsed := time.Since(start).Milliseconds()

		stepStatus := model.StatusDeployed
		if err != nil {
			stepStatus = model.StatusFailed
		}

		deploy.Steps = append(deploy.Steps, model.StepLog{
			Step:       s.name,
			Status:     stepStatus,
			DurationMs: elapsed,
			Output:     output,
		})

		p.WS.Broadcast(hub.Event{Type: "deploy.step", AppID: deploy.App, Payload: map[string]string{
			"step":       s.name,
			"status":     string(stepStatus),
			"output":     output,
			"durationMs": fmt.Sprintf("%d", elapsed),
		}})

		if err != nil {
			deploy.Status = model.StatusFailed
			deploy.Error = fmt.Sprintf("%s: %v", s.name, err)
			p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status, deploy.Steps, deploy.Error)
			p.WS.Broadcast(hub.Event{Type: "deploy.failed", AppID: deploy.App, Payload: deploy})
			// Clean up work dir on failure
			if deploy.WorkDir != "" {
				os.RemoveAll(deploy.WorkDir)
			}
			return
		}
	}

	deploy.Status = model.StatusDeployed
	p.DB.UpdateDeployment(ctx, deploy.ID, deploy.Status, deploy.Steps, "")
	p.WS.Broadcast(hub.Event{Type: "deploy.completed", AppID: deploy.App, Payload: deploy})
}

type step struct {
	name string
	fn   func(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error)
}

func (p *Pipeline) clone(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	workDir, err := os.MkdirTemp("", "norn-build-*")
	if err != nil {
		return "", fmt.Errorf("create temp dir: %w", err)
	}
	d.WorkDir = workDir

	if s.Repo != nil {
		args := []string{"clone", "--depth", "1", "--branch", s.Repo.Branch, s.Repo.URL, workDir}
		cmd := exec.CommandContext(ctx, "git", args...)
		gitEnv, cleanup := p.gitEnv(s.Repo.URL)
		if cleanup != nil {
			defer cleanup()
		}
		cmd.Env = append(os.Environ(), gitEnv...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			// Fall back to local copy if repo is unreachable
			srcDir := filepath.Join(p.AppsDir, d.App)
			if _, statErr := os.Stat(srcDir); statErr == nil {
				log.Printf("clone: git clone failed (%v), falling back to local copy from %s", err, srcDir)
				cpCmd := exec.CommandContext(ctx, "cp", "-a", srcDir+"/.", workDir)
				cpOut, cpErr := cpCmd.CombinedOutput()
				if cpErr != nil {
					return string(cpOut), fmt.Errorf("git clone failed and local fallback failed: %w", cpErr)
				}
				// Resolve SHA from local source git repo
				revCmd := exec.CommandContext(ctx, "git", "-C", srcDir, "rev-parse", "HEAD")
				if shaOut, revErr := revCmd.Output(); revErr == nil {
					sha := strings.TrimSpace(string(shaOut))
					d.CommitSHA = sha
					d.ImageTag = fmt.Sprintf("%s:%s", d.App, sha[:min(12, len(sha))])
				} else {
					ts := time.Now().Format("20060102150405")
					d.CommitSHA = "local-" + ts
					d.ImageTag = fmt.Sprintf("%s:local-%s", d.App, ts)
				}
				return fmt.Sprintf("git clone failed, used local copy from %s", srcDir), nil
			}
			return string(out), fmt.Errorf("git clone: %w", err)
		}

		// Resolve actual HEAD SHA from the cloned repo
		revCmd := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "HEAD")
		shaOut, err := revCmd.Output()
		if err == nil {
			sha := strings.TrimSpace(string(shaOut))
			d.CommitSHA = sha
			d.ImageTag = fmt.Sprintf("%s:%s", d.App, sha[:12])
		}

		return fmt.Sprintf("cloned %s (branch %s) at %s", s.Repo.URL, s.Repo.Branch, d.CommitSHA[:min(12, len(d.CommitSHA))]), nil
	}

	// Local fallback: copy from appsDir
	srcDir := filepath.Join(p.AppsDir, d.App)
	cmd := exec.CommandContext(ctx, "cp", "-a", srcDir+"/.", workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("copy source: %w", err)
	}
	return fmt.Sprintf("copied %s into %s", srcDir, workDir), nil
}

func (p *Pipeline) gitEnv(url string) (env []string, cleanup func()) {
	if isSSHURL(url) && p.GitSSHKey != "" {
		return []string{
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=accept-new", p.GitSSHKey),
		}, nil
	}
	if !isSSHURL(url) && p.GitToken != "" {
		// Write a temp askpass script
		script, err := os.CreateTemp("", "norn-askpass-*")
		if err != nil {
			log.Printf("WARNING: could not create askpass script: %v", err)
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

func (p *Pipeline) build(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Build == nil {
		return "skipped (no build spec)", nil
	}
	workDir := d.WorkDir
	if workDir == "" {
		workDir = "."
	}
	cmd := exec.CommandContext(ctx, "docker", "build",
		"--build-arg", fmt.Sprintf("VERSION=%s", d.CommitSHA),
		"-t", d.ImageTag, workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}

	// Push to container registry if configured
	if p.RegistryURL != "" {
		registryTag := fmt.Sprintf("%s/%s", p.RegistryURL, d.ImageTag)
		tagCmd := exec.CommandContext(ctx, "docker", "tag", d.ImageTag, registryTag)
		if tagOut, tagErr := tagCmd.CombinedOutput(); tagErr != nil {
			return string(out), fmt.Errorf("docker tag: %s", string(tagOut))
		}
		pushCmd := exec.CommandContext(ctx, "docker", "push", registryTag)
		pushOut, pushErr := pushCmd.CombinedOutput()
		if pushErr != nil {
			return string(out), fmt.Errorf("docker push: %s", string(pushOut))
		}
		d.ImageTag = registryTag
		return string(out) + "\npushed image to " + registryTag, nil
	}

	// Legacy: load image into minikube if available
	if _, lookErr := exec.LookPath("minikube"); lookErr == nil {
		loadCmd := exec.CommandContext(ctx, "minikube", "image", "load", d.ImageTag)
		loadOut, loadErr := loadCmd.CombinedOutput()
		if loadErr != nil {
			return string(out), fmt.Errorf("minikube image load: %s", string(loadOut))
		}
		return string(out) + "\nloaded image into minikube", nil
	}

	return string(out), nil
}

func (p *Pipeline) test(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Build == nil || s.Build.Test == "" {
		return "skipped (no test command)", nil
	}
	workDir := d.WorkDir
	if workDir == "" {
		return "", fmt.Errorf("no work dir for test")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Build.Test)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) snapshot(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Services == nil || s.Services.Postgres == nil {
		return "skipped (no postgres)", nil
	}
	db := s.Services.Postgres.Database
	sha := d.CommitSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	filename := fmt.Sprintf("snapshots/%s_%s_%s.dump", db, sha, time.Now().Format("20060102T150405"))
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return "", fmt.Errorf("create snapshots dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", db, "-f", filename)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) migrate(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Migrations == nil || s.Migrations.Command == "" {
		return "skipped (no migrations)", nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Migrations.Command)
	if d.WorkDir != "" {
		cmd.Dir = d.WorkDir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) deploy(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.IsCron() && p.Scheduler != nil {
		p.Scheduler.SetImage(d.App, d.ImageTag)
		return fmt.Sprintf("registered image %s with cron scheduler", d.ImageTag), nil
	}
	err := p.Kube.SetImage(ctx, "default", d.App, d.App, d.ImageTag)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("set image to %s", d.ImageTag), nil
}

func (p *Pipeline) cleanup(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if d.WorkDir == "" {
		return "skipped (no work dir)", nil
	}
	if err := os.RemoveAll(d.WorkDir); err != nil {
		return "", fmt.Errorf("remove work dir: %w", err)
	}
	return fmt.Sprintf("removed %s", d.WorkDir), nil
}

func statusForStep(name string) model.DeployStatus {
	switch name {
	case "clone":
		return model.StatusBuilding
	case "build":
		return model.StatusBuilding
	case "test":
		return model.StatusTesting
	case "snapshot":
		return model.StatusSnapshot
	case "migrate":
		return model.StatusMigrating
	case "deploy":
		return model.StatusDeploying
	case "cleanup":
		return model.StatusDeploying
	default:
		return model.StatusQueued
	}
}
