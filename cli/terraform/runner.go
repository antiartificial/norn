package terraform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-exec/tfexec"
)

type Runner struct {
	tf      *tfexec.Terraform
	workDir string
}

func NewRunner() (*Runner, error) {
	// Find terraform binary
	execPath, err := findTerraform()
	if err != nil {
		return nil, err
	}

	// Resolve norn infra/terraform directory
	home, _ := os.UserHomeDir()
	workDir := filepath.Join(home, "projects", "norn", "infra", "terraform")

	tf, err := tfexec.NewTerraform(workDir, execPath)
	if err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}

	return &Runner{tf: tf, workDir: workDir}, nil
}

func findTerraform() (string, error) {
	// Check common locations
	candidates := []string{
		"/opt/homebrew/bin/terraform",
		"/usr/local/bin/terraform",
		"/usr/bin/terraform",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("terraform not found in common locations; install via: brew install terraform")
}

func (r *Runner) Init(ctx context.Context) error {
	return r.tf.Init(ctx)
}

func (r *Runner) Apply(ctx context.Context, targets ...string) error {
	opts := []tfexec.ApplyOption{tfexec.Lock(true)}
	for _, t := range targets {
		opts = append(opts, tfexec.Target(t))
	}
	return r.tf.Apply(ctx, opts...)
}

func (r *Runner) Destroy(ctx context.Context, targets ...string) error {
	opts := []tfexec.DestroyOption{tfexec.Lock(true)}
	for _, t := range targets {
		opts = append(opts, tfexec.Target(t))
	}
	return r.tf.Destroy(ctx, opts...)
}

func (r *Runner) Plan(ctx context.Context) (bool, error) {
	return r.tf.Plan(ctx)
}

func (r *Runner) Output(ctx context.Context) (map[string]tfexec.OutputMeta, error) {
	return r.tf.Output(ctx)
}

func (r *Runner) WorkDir() string {
	return r.workDir
}
