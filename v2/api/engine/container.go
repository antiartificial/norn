package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

const containerBin = "container"

func containerCmd(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, containerBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", containerBin, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

type containerListEntry struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	IP      string `json:"ip"`
	Created string `json:"created"`
}

func containerList(ctx context.Context) ([]containerListEntry, error) {
	out, err := containerCmd(ctx, "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var entries []containerListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}
	return entries, nil
}

type containerInspectResult struct {
	Name     string `json:"name"`
	Image    string `json:"image"`
	Status   string `json:"status"`
	IP       string `json:"ip"`
	ExitCode *int   `json:"exitCode"`
	Created  string `json:"created"`
}

func containerInspect(ctx context.Context, name string) (*containerInspectResult, error) {
	out, err := containerCmd(ctx, "inspect", name, "--format", "json")
	if err != nil {
		return nil, err
	}
	var result containerInspectResult
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse container inspect: %w", err)
	}
	return &result, nil
}

type RunOpts struct {
	Name     string
	Image    string
	Command  string
	Env      map[string]string
	CPUs     int
	MemoryMB int
	Volumes  []VolumeMount
	Port     int
}

type VolumeMount struct {
	Source   string
	Target  string
	ReadOnly bool
}

func containerRun(ctx context.Context, opts RunOpts) error {
	args := []string{"run", "-d", "--name", opts.Name}

	if opts.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", opts.CPUs))
	}
	if opts.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dMB", opts.MemoryMB))
	}
	if opts.Port > 0 {
		args = append(args, "-p", fmt.Sprintf("%d:%d", opts.Port, opts.Port))
	}
	for _, v := range opts.Volumes {
		mount := fmt.Sprintf("%s:%s", v.Source, v.Target)
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	for k, v := range opts.Env {
		args = append(args, "--env", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, opts.Image)
	if opts.Command != "" {
		args = append(args, "/bin/sh", "-c", opts.Command)
	}

	_, err := containerCmd(ctx, args...)
	return err
}

func containerStop(ctx context.Context, name string, timeout time.Duration) error {
	args := []string{"stop"}
	if timeout > 0 {
		args = append(args, "--time", fmt.Sprintf("%d", int(timeout.Seconds())))
	}
	args = append(args, name)
	_, err := containerCmd(ctx, args...)
	return err
}

func containerRemove(ctx context.Context, name string) error {
	_, err := containerCmd(ctx, "rm", name)
	return err
}

func containerStopAndRemove(ctx context.Context, name string, timeout time.Duration) {
	if err := containerStop(ctx, name, timeout); err != nil {
		log.Printf("engine: stop %s: %v", name, err)
	}
	if err := containerRemove(ctx, name); err != nil {
		log.Printf("engine: rm %s: %v", name, err)
	}
}
