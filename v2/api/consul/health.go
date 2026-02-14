package consul

import (
	consulapi "github.com/hashicorp/consul/api"
)

// ServiceHealth represents the health status of a service instance.
type ServiceHealth struct {
	ServiceName string `json:"serviceName"`
	Node        string `json:"node"`
	Address     string `json:"address"`
	Port        int    `json:"port"`
	Status      string `json:"status"` // passing, warning, critical
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
		results = append(results, ServiceHealth{
			ServiceName: entry.Service.Service,
			Node:        entry.Node.Node,
			Address:     entry.Service.Address,
			Port:        entry.Service.Port,
			Status:      status,
		})
	}
	return results, nil
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
