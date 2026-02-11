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

	"norn/api/hub"
	"norn/api/k8s"
	"norn/api/model"
	"norn/api/store"
)

type Pipeline struct {
	DB        *store.DB
	Kube      *k8s.Client
	WS        *hub.Hub
	AppsDir   string
	GitToken  string
	GitSSHKey string
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
		cmd.Env = append(os.Environ(), p.gitEnv(s.Repo.URL)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("git clone: %w", err)
		}
		return fmt.Sprintf("cloned %s (branch %s) into %s", s.Repo.URL, s.Repo.Branch, workDir), nil
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

func (p *Pipeline) gitEnv(url string) []string {
	if isSSHURL(url) && p.GitSSHKey != "" {
		return []string{
			fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o StrictHostKeyChecking=no", p.GitSSHKey),
		}
	}
	if !isSSHURL(url) && p.GitToken != "" {
		// Write a temp askpass script
		script, err := os.CreateTemp("", "norn-askpass-*")
		if err != nil {
			log.Printf("WARNING: could not create askpass script: %v", err)
			return nil
		}
		fmt.Fprintf(script, "#!/bin/sh\necho '%s'\n", p.GitToken)
		script.Close()
		os.Chmod(script.Name(), 0700)
		return []string{
			"GIT_ASKPASS=" + script.Name(),
			"GIT_TERMINAL_PROMPT=0",
		}
	}
	return nil
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
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", d.ImageTag, workDir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) test(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Build == nil || s.Build.Test == "" {
		return "skipped (no test command)", nil
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", d.ImageTag, "sh", "-c", s.Build.Test)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) snapshot(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Services == nil || s.Services.Postgres == nil {
		return "skipped (no postgres)", nil
	}
	db := s.Services.Postgres.Database
	filename := fmt.Sprintf("snapshots/%s_%s_%s.dump", db, d.CommitSHA[:12], time.Now().Format("20060102T150405"))
	cmd := exec.CommandContext(ctx, "pg_dump", "-Fc", "-d", db, "-f", filename)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) migrate(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
	if s.Migrations == nil || s.Migrations.Command == "" {
		return "skipped (no migrations)", nil
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Migrations.Command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *Pipeline) deploy(ctx context.Context, d *model.Deployment, s *model.InfraSpec) (string, error) {
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
