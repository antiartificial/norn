package nomad

import (
	"context"
	"fmt"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

// SubmitJob registers a job with Nomad.
func (c *Client) SubmitJob(job *nomadapi.Job) (string, error) {
	resp, _, err := c.api.Jobs().Register(job, nil)
	if err != nil {
		return "", fmt.Errorf("submit job: %w", err)
	}
	return resp.EvalID, nil
}

// StopJob stops a running Nomad job.
func (c *Client) StopJob(jobID string, purge bool) error {
	_, _, err := c.api.Jobs().Deregister(jobID, purge, nil)
	return err
}

// RestartJob forces a restart by creating a new evaluation.
func (c *Client) RestartJob(jobID string) error {
	job, _, err := c.api.Jobs().Info(jobID, nil)
	if err != nil {
		return fmt.Errorf("get job info: %w", err)
	}
	_, _, err = c.api.Jobs().Register(job, nil)
	return err
}

// JobStatus returns the status of a Nomad job.
func (c *Client) JobStatus(jobID string) (string, error) {
	job, _, err := c.api.Jobs().Info(jobID, nil)
	if err != nil {
		return "", err
	}
	if job.Status == nil {
		return "unknown", nil
	}
	return *job.Status, nil
}

// JobAllocations returns allocations for a job.
func (c *Client) JobAllocations(jobID string) ([]*nomadapi.AllocationListStub, error) {
	allocs, _, err := c.api.Jobs().Allocations(jobID, false, nil)
	if err != nil {
		return nil, err
	}
	return allocs, nil
}

// WaitHealthy waits until all allocations for a job report healthy.
// Returns an error if the timeout is exceeded.
func (c *Client) WaitHealthy(ctx context.Context, jobID string, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout waiting for %s to become healthy", jobID)
		case <-ticker.C:
			allocs, err := c.JobAllocations(jobID)
			if err != nil {
				continue
			}
			if len(allocs) == 0 {
				continue
			}

			healthy := 0
			pending := 0
			for _, alloc := range allocs {
				// Skip terminal allocations from previous deploys
				if alloc.ClientStatus == "complete" || alloc.ClientStatus == "failed" || alloc.ClientStatus == "lost" {
					continue
				}
				if alloc.ClientStatus != "running" {
					pending++
					continue
				}
				if alloc.DeploymentStatus == nil || alloc.DeploymentStatus.Healthy == nil || !*alloc.DeploymentStatus.Healthy {
					pending++
					continue
				}
				healthy++
			}
			if healthy > 0 && pending == 0 {
				return nil
			}
		}
	}
}

// AllocStatus is a summary of a single non-terminal allocation.
type AllocStatus struct {
	ID           string `json:"id"`           // first 8 chars
	TaskGroup    string `json:"taskGroup"`
	ClientStatus string `json:"clientStatus"` // pending, running
	Healthy      *bool  `json:"healthy"`
	NodeID       string `json:"nodeId"`   // first 8 chars
	NodeName     string `json:"nodeName"`
}

// PollAllocations returns non-terminal allocations for a job (single poll).
func (c *Client) PollAllocations(jobID string) ([]AllocStatus, error) {
	allocs, err := c.JobAllocations(jobID)
	if err != nil {
		return nil, err
	}
	var out []AllocStatus
	for _, a := range allocs {
		if a.ClientStatus == "complete" || a.ClientStatus == "failed" || a.ClientStatus == "lost" {
			continue
		}
		as := AllocStatus{
			ID:           short(a.ID, 8),
			TaskGroup:    a.TaskGroup,
			ClientStatus: a.ClientStatus,
			NodeID:       short(a.NodeID, 8),
			NodeName:     a.NodeID, // enrichable later via node cache
		}
		if a.DeploymentStatus != nil {
			as.Healthy = a.DeploymentStatus.Healthy
		}
		out = append(out, as)
	}
	return out, nil
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// NodeInfo describes where a Nomad node is running.
type NodeInfo struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Provider string `json:"provider"` // local, do, hz, remote
	Region   string `json:"region"`   // sfo3, fsn1, etc.
}

// NodeInfo returns location information for a Nomad node.
func (c *Client) NodeInfo(nodeID string) (*NodeInfo, error) {
	node, _, err := c.api.Nodes().Info(nodeID, nil)
	if err != nil {
		return nil, err
	}

	addr := node.HTTPAddr
	if ip, ok := node.Attributes["unique.network.ip-address"]; ok {
		addr = ip
	}

	info := &NodeInfo{
		Name:    node.Name,
		Address: addr,
	}

	// Detect provider from node attributes
	if _, ok := node.Attributes["unique.platform.digitalocean.id"]; ok {
		info.Provider = "do"
		info.Region = node.Attributes["platform.digitalocean.region"]
	} else if _, ok := node.Attributes["unique.platform.hetzner.id"]; ok {
		info.Provider = "hz"
		info.Region = node.Attributes["platform.hetzner.datacenter"]
	} else if osName, ok := node.Attributes["os.name"]; ok && osName == "darwin" {
		info.Provider = "local"
	} else {
		info.Provider = "remote"
	}

	return info, nil
}

// ScaleJob updates the count for a specific task group.
func (c *Client) ScaleJob(jobID, group string, count int) error {
	_, _, err := c.api.Jobs().Scale(jobID, group, &count, "scaled via norn", false, nil, nil)
	return err
}
