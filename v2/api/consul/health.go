package consul

import (
	"strings"

	consulapi "github.com/hashicorp/consul/api"
)

// ServiceHealth represents the health status of a service instance.
type ServiceHealth struct {
	ServiceName       string `json:"serviceName"`
	ID                string `json:"id"`
	AllocationID      string `json:"allocationId,omitempty"`
	Node              string `json:"node"`
	Address           string `json:"address"`
	Port              int    `json:"port"`
	Status            string `json:"status"` // passing, warning, critical
	Region            string `json:"region,omitempty"`
	NodePool          string `json:"nodePool,omitempty"`
	PlacementVerified bool   `json:"placementVerified"`
}

// ServiceHealthChecks returns health check results for a named service.
func (c *Client) ServiceHealthChecks(serviceName string) ([]ServiceHealth, error) {
	entries, _, err := c.api.Health().Service(serviceName, "", false, nil)
	if err != nil {
		return nil, err
	}

	var results []ServiceHealth
	for _, entry := range entries {
		status := aggregateChecks(entry.Checks)
		region := serviceTagValue(entry.Service.Tags, "norn.region")
		allocationID := serviceTagValue(entry.Service.Tags, "norn.allocation")
		nodePool := serviceTagValue(entry.Service.Tags, "norn.node-pool")
		results = append(results, ServiceHealth{
			ServiceName:       entry.Service.Service,
			ID:                entry.Service.ID,
			AllocationID:      allocationID,
			Node:              entry.Node.Node,
			Address:           entry.Service.Address,
			Port:              entry.Service.Port,
			Status:            status,
			Region:            region,
			NodePool:          nodePool,
			PlacementVerified: region != "" && allocationID != "" && nodePool != "",
		})
	}
	return results, nil
}

func serviceTagValue(tags []string, key string) string {
	prefix := key + "="
	for _, tag := range tags {
		if strings.HasPrefix(tag, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(tag, prefix))
		}
	}
	return ""
}

func aggregateChecks(checks consulapi.HealthChecks) string {
	worst := "passing"
	for _, check := range checks {
		switch check.Status {
		case "critical":
			return "critical"
		case "warning":
			worst = "warning"
		}
	}
	return worst
}
