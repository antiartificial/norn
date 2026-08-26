package consul

import (
	"fmt"

	consulapi "github.com/hashicorp/consul/api"
)

type Client struct {
	api *consulapi.Client
}

func NewClient(addr string) (*Client, error) {
	cfg := consulapi.DefaultConfig()
	cfg.Address = addr

	client, err := consulapi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("consul client: %w", err)
	}
	return &Client{api: client}, nil
}

// Healthy checks connectivity to Consul.
func (c *Client) Healthy() error {
	_, err := c.api.Status().Leader()
	return err
}

// API returns the underlying Consul API client.
func (c *Client) API() *consulapi.Client {
	return c.api
}
