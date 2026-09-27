package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"norn/v2/api/nomad"
)

var routeHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)

type WeightedBackend struct {
	DeploymentID string
	Weight       int
}

type WeightedRoute struct {
	App      string
	Process  string
	Region   string
	Endpoint string
	Backends []WeightedBackend
}

type RenderedRoute struct {
	YAML         []byte
	SHA256       string
	RouterName   string
	ServiceName  string
	EndpointHost string
	BackendNames []string
}

type routeDocument struct {
	HTTP struct {
		Routers  map[string]routeRouter  `yaml:"routers"`
		Services map[string]routeService `yaml:"services"`
	} `yaml:"http"`
}

type routeRouter struct {
	Rule        string    `yaml:"rule"`
	EntryPoints []string  `yaml:"entryPoints"`
	Service     string    `yaml:"service"`
	TLS         *struct{} `yaml:"tls,omitempty"`
}

type routeService struct {
	Weighted struct {
		Services []routeBackend `yaml:"services"`
	} `yaml:"weighted"`
}

type routeBackend struct {
	Name   string `yaml:"name"`
	Weight int    `yaml:"weight"`
}

// RenderWeightedRoute produces one desired Traefik file-provider route. It
// does not apply the file or assert that any ingress node has loaded it.
func RenderWeightedRoute(input WeightedRoute) (RenderedRoute, error) {
	if input.App == "" || input.Process == "" || input.Region == "" || len(input.Backends) == 0 {
		return RenderedRoute{}, fmt.Errorf("weighted route identity or backends are missing")
	}
	endpoint, err := url.Parse(input.Endpoint)
	if err != nil || endpoint == nil || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" || endpoint.RawPath != "" || endpoint.Port() != "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return RenderedRoute{}, fmt.Errorf("weighted route endpoint must be an origin URL")
	}
	host := strings.ToLower(endpoint.Hostname())
	if len(host) > 253 || !routeHostname.MatchString(host) {
		return RenderedRoute{}, fmt.Errorf("weighted route endpoint host is invalid")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > 63 {
			return RenderedRoute{}, fmt.Errorf("weighted route endpoint label is too long")
		}
	}
	identity := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%d:%s%d:%s%d:%s", len(input.App), input.App, len(input.Process), input.Process, len(input.Region), input.Region, len(host), host)))
	name := "norn-route-" + hex.EncodeToString(identity[:16])
	doc := routeDocument{}
	doc.HTTP.Routers = map[string]routeRouter{}
	doc.HTTP.Services = map[string]routeService{}
	router := routeRouter{Rule: "Host(`" + host + "`)", Service: name}
	if endpoint.Scheme == "https" {
		router.EntryPoints = []string{"websecure"}
		router.TLS = &struct{}{}
	} else {
		router.EntryPoints = []string{"web"}
	}
	doc.HTTP.Routers[name] = router
	service := routeService{}
	seen := map[string]bool{}
	total := 0
	names := make([]string, 0, len(input.Backends))
	backends := append([]WeightedBackend(nil), input.Backends...)
	sort.Slice(backends, func(i, j int) bool { return backends[i].DeploymentID < backends[j].DeploymentID })
	for _, backend := range backends {
		if backend.DeploymentID == "" || seen[backend.DeploymentID] || backend.Weight <= 0 || backend.Weight > 100 {
			return RenderedRoute{}, fmt.Errorf("weighted route backend is invalid")
		}
		seen[backend.DeploymentID] = true
		total += backend.Weight
		backendName, err := nomad.ManagedBackendServiceName(input.App, input.Process, input.Region, backend.DeploymentID)
		if err != nil {
			return RenderedRoute{}, err
		}
		names = append(names, backendName)
		service.Weighted.Services = append(service.Weighted.Services, routeBackend{Name: backendName + "@consulcatalog", Weight: backend.Weight})
	}
	if total != 100 {
		return RenderedRoute{}, fmt.Errorf("weighted route backends must total 100")
	}
	doc.HTTP.Services[name] = service
	encoded, err := yaml.Marshal(doc)
	if err != nil {
		return RenderedRoute{}, err
	}
	digest := sha256.Sum256(encoded)
	return RenderedRoute{YAML: encoded, SHA256: hex.EncodeToString(digest[:]), RouterName: name, ServiceName: name, EndpointHost: host, BackendNames: names}, nil
}
