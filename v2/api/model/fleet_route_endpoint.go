package model

import (
	"fmt"
	"net/url"
)

// FleetRouteEndpoint is the unambiguous HTTPS service origin in a pinned
// InfraSpec. Control-store route intent and workers must derive it from the
// spec rather than accept endpoint or process values from a request.
type FleetRouteEndpoint struct {
	Origin  string
	Process string
	Port    int
}

func ResolveFleetRouteEndpoint(spec *InfraSpec, region string) (FleetRouteEndpoint, error) {
	if spec == nil || region == "" {
		return FleetRouteEndpoint{}, fmt.Errorf("fleet route source or region is missing")
	}
	var selected *Endpoint
	for i := range spec.Endpoints {
		candidate := &spec.Endpoints[i]
		if candidate.Region != region {
			continue
		}
		if selected != nil {
			return FleetRouteEndpoint{}, fmt.Errorf("fleet route endpoint is ambiguous")
		}
		selected = candidate
	}
	if selected == nil || selected.Process == "" {
		return FleetRouteEndpoint{}, fmt.Errorf("fleet route endpoint or process binding is missing")
	}
	process, ok := spec.Processes[selected.Process]
	if !ok || process.Port <= 0 || process.Schedule != "" || process.Function != nil {
		return FleetRouteEndpoint{}, fmt.Errorf("fleet route process is not a service with a port")
	}
	origin, err := url.Parse(selected.URL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.Port() != "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || origin.RawPath != "" {
		return FleetRouteEndpoint{}, fmt.Errorf("fleet route endpoint must be an HTTPS origin")
	}
	return FleetRouteEndpoint{Origin: selected.URL, Process: selected.Process, Port: process.Port}, nil
}
