package model

import "time"

type ServiceManifest struct {
	Version     int                     `json:"version"`
	GeneratedAt time.Time               `json:"generatedAt"`
	NetworkMode string                  `json:"networkMode,omitempty"`
	Contract    ServiceManifestContract `json:"contract"`
	Services    []ServiceManifestEntry  `json:"services"`
}

type ServiceManifestEntry struct {
	Name         string              `json:"name"`
	App          string              `json:"app"`
	Process      string              `json:"process"`
	Type         string              `json:"type"`
	Status       string              `json:"status"`
	HealthPath   string              `json:"healthPath,omitempty"`
	Reachability ServiceReachability `json:"reachability"`
	Endpoints    []Endpoint          `json:"endpoints,omitempty"`
	Instances    []ServiceInstance   `json:"instances,omitempty"`
	Metadata     map[string]string   `json:"metadata,omitempty"`
}

type ServiceManifestContract struct {
	Schema             string   `json:"schema"`
	ProcessTypes       []string `json:"processTypes"`
	ReachabilityScopes []string `json:"reachabilityScopes"`
}

type ServiceReachability struct {
	EndpointScope string `json:"endpointScope"`
	InstanceScope string `json:"instanceScope"`
	Exposure      string `json:"exposure"`
	Routable      bool   `json:"routable"`
}

type ServiceInstance struct {
	ID      string `json:"id,omitempty"`
	Node    string `json:"node,omitempty"`
	Address string `json:"address,omitempty"`
	Port    int    `json:"port,omitempty"`
	Status  string `json:"status,omitempty"`
}
