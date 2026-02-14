package nomad

import (
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"
)

type Client struct {
	api *nomadapi.Client
}

func NewClient(addr string) (*Client, error) {
	cfg := nomadapi.DefaultConfig()
	cfg.Address = addr

	client, err := nomadapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("nomad client: %w", err)
	}
	return &Client{api: client}, nil
}

// Healthy checks connectivity to Nomad.
func (c *Client) Healthy() error {
	_, err := c.api.Agent().NodeName()
	return err
}

// API returns the underlying Nomad API client for advanced usage.
func (c *Client) API() *nomadapi.Client {
	return c.api
}
