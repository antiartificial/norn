package model

import "time"

type ClusterNode struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Provider    string    `json:"provider"`    // hetzner, digitalocean, vultr, local
	Region      string    `json:"region"`
	Size        string    `json:"size"`
	Role        string    `json:"role"`         // server, agent
	PublicIP    string    `json:"publicIp"`
	TailscaleIP string    `json:"tailscaleIp"`
	Status      string    `json:"status"`       // provisioning, ready, draining, removed, failed
	ProviderID  string    `json:"providerId"`   // cloud resource ID
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}
