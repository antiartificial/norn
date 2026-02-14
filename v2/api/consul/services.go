package consul

// ServiceEntry represents a registered service in Consul.
type ServiceEntry struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

// ListServices returns all registered services from the Consul catalog.
func (c *Client) ListServices() ([]ServiceEntry, error) {
	services, _, err := c.api.Catalog().Services(nil)
	if err != nil {
		return nil, err
	}

	var entries []ServiceEntry
	for name, tags := range services {
		if name == "consul" {
			continue
		}
		entries = append(entries, ServiceEntry{Name: name, Tags: tags})
	}
	return entries, nil
}

// ServiceInstances returns the instances (nodes) registered for a service.
type ServiceInstance struct {
	ID      string `json:"id"`
	Node    string `json:"node"`
	Address string `json:"address"`
	Port    int    `json:"port"`
}

func (c *Client) ServiceInstances(serviceName string) ([]ServiceInstance, error) {
	services, _, err := c.api.Catalog().Service(serviceName, "", nil)
	if err != nil {
		return nil, err
	}

	var instances []ServiceInstance
	for _, svc := range services {
		instances = append(instances, ServiceInstance{
			ID:      svc.ServiceID,
			Node:    svc.Node,
			Address: svc.ServiceAddress,
			Port:    svc.ServicePort,
		})
	}
	return instances, nil
}
