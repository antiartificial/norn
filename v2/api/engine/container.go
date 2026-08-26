package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const containerBin = "container"

// Apple Container rejects explicit memory limits below 200 MiB. InfraSpecs
// use portable scheduler-sized requests (often 64 or 128 MiB), so the local
// development connector raises only the runtime limit to Apple's floor rather
// than failing an otherwise portable application at submit time.
const minimumContainerMemoryMB = 200

var runContainerCommand = defaultContainerCommand

func containerCmd(ctx context.Context, args ...string) ([]byte, error) {
	return runContainerCommand(ctx, args...)
}

func defaultContainerCommand(ctx context.Context, args ...string) ([]byte, error) {
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

func (entry *containerListEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID            string          `json:"id"`
		Name          string          `json:"name"`
		Image         string          `json:"image"`
		Status        json.RawMessage `json:"status"`
		IP            string          `json:"ip"`
		Created       string          `json:"created"`
		Configuration struct {
			ID           string `json:"id"`
			CreationDate string `json:"creationDate"`
			Image        struct {
				Reference string `json:"reference"`
			} `json:"image"`
		} `json:"configuration"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	entry.Name, entry.Image, entry.IP, entry.Created = raw.Name, raw.Image, raw.IP, raw.Created
	if entry.Name == "" {
		entry.Name = raw.Configuration.ID
	}
	if entry.Name == "" {
		entry.Name = raw.ID
	}
	if entry.Image == "" {
		entry.Image = raw.Configuration.Image.Reference
	}
	if entry.Created == "" {
		entry.Created = raw.Configuration.CreationDate
	}
	if err := json.Unmarshal(raw.Status, &entry.Status); err != nil {
		var status struct {
			State       string `json:"state"`
			StartedDate string `json:"startedDate"`
			Networks    []struct {
				IPv4Address string `json:"ipv4Address"`
				Address     string `json:"address"`
			} `json:"networks"`
		}
		if objectErr := json.Unmarshal(raw.Status, &status); objectErr != nil {
			return fmt.Errorf("parse container status: %w", objectErr)
		}
		entry.Status = status.State
		if status.StartedDate != "" {
			entry.Created = status.StartedDate
		}
		if len(status.Networks) > 0 {
			entry.IP = status.Networks[0].IPv4Address
			if entry.IP == "" {
				entry.IP = status.Networks[0].Address
			}
			if slash := strings.IndexByte(entry.IP, '/'); slash >= 0 {
				entry.IP = entry.IP[:slash]
			}
		}
	}
	return nil
}

func containerList(ctx context.Context) ([]containerListEntry, error) {
	out, err := containerCmd(ctx, "list", "--all", "--format", "json")
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
	out, err := containerCmd(ctx, "inspect", name)
	if err != nil {
		return nil, err
	}
	var entries []containerListEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return nil, fmt.Errorf("parse container inspect: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("container inspect returned no result for %s", name)
	}
	entry := entries[0]
	return &containerInspectResult{Name: entry.Name, Image: entry.Image, Status: entry.Status, IP: entry.IP, Created: entry.Created}, nil
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
	HostPort int
}

type VolumeMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

func containerRun(ctx context.Context, opts RunOpts) error {
	args := []string{"run", "-d", "--name", opts.Name}

	if opts.CPUs > 0 {
		args = append(args, "--cpus", fmt.Sprintf("%d", opts.CPUs))
	}
	if opts.MemoryMB > 0 {
		memoryMB := opts.MemoryMB
		if memoryMB < minimumContainerMemoryMB {
			memoryMB = minimumContainerMemoryMB
		}
		args = append(args, "--memory", fmt.Sprintf("%dMB", memoryMB))
	}
	if opts.Port > 0 {
		hostPort := opts.HostPort
		if hostPort == 0 {
			hostPort = opts.Port
		}
		args = append(args, "--publish", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, opts.Port))
	}
	for _, v := range opts.Volumes {
		mount := fmt.Sprintf("%s:%s", v.Source, v.Target)
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}
	if len(opts.Env) > 0 {
		envFile, err := os.CreateTemp("", "norn-container-env-*.env")
		if err != nil {
			return fmt.Errorf("create private environment file: %w", err)
		}
		envPath := envFile.Name()
		defer os.Remove(envPath)
		if err := envFile.Chmod(0o600); err != nil {
			_ = envFile.Close()
			return fmt.Errorf("protect environment file: %w", err)
		}
		keys := make([]string, 0, len(opts.Env))
		for key := range opts.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := strings.ReplaceAll(opts.Env[key], "\n", "\\n")
			if _, err := fmt.Fprintf(envFile, "%s=%s\n", key, value); err != nil {
				_ = envFile.Close()
				return fmt.Errorf("write environment file: %w", err)
			}
		}
		if err := envFile.Close(); err != nil {
			return fmt.Errorf("close environment file: %w", err)
		}
		args = append(args, "--env-file", envPath)
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

func containerStart(ctx context.Context, name string) error {
	_, err := containerCmd(ctx, "start", name)
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
