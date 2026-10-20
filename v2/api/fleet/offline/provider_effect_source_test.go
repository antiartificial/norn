package offline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

type providerEffectTransport func(*http.Request) (*http.Response, error)

func (fn providerEffectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestDigitalOceanEffectSourceExactIndependentTranscript(t *testing.T) {
	b := goodBinding()
	b.SchemaVersion = BindingSchemaV2
	b.CandidateSHA256 = strings.Repeat("a", 64)
	b.ProviderAccountUUID = "17f40050233d1cd4962b44050e82a0dd01f8dff7"
	for i, name := range workNodeNames {
		id := strconv.Itoa(i + 1)
		if name == WorkNode {
			id = WorkDropletID
		}
		b.ProviderNodes = append(b.ProviderNodes, ProviderNode{Name: name, ProviderID: id})
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	project := "11111111-1111-4111-8111-111111111111"
	base := DigitalOceanEffectSource{ProjectID: project, VPCUUID: WorkVPCUUID, RequiredTags: []string{"norn-work-staging"}, App03PrivateIP: "10.43.0.13", App03PublicIP: "159.203.100.16", Token: func(context.Context) (string, error) { return "secret-token", nil }}
	fixtures := func(mutate func(string, map[string]any)) (DigitalOceanEffectSource, *int) {
		calls := 0
		s := base
		s.Client = &http.Client{Transport: providerEffectTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.Method != http.MethodGet || req.URL.Host != "api.digitalocean.com" || req.Header.Get("Authorization") != "Bearer secret-token" {
				t.Errorf("untrusted provider request: %s", req.URL)
				return nil, ErrProviderEffectSource
			}
			path := req.URL.RequestURI()
			var body map[string]any
			switch {
			case path == "/v2/account":
				body = map[string]any{"account": map[string]any{"uuid": b.ProviderAccountUUID, "extraSecret": "not retained"}}
			case path == "/v2/projects/"+project+"/resources?per_page=200&page=1":
				resources := make([]any, 0, 8)
				for _, n := range b.ProviderNodes {
					resources = append(resources, map[string]any{"urn": "do:droplet:" + n.ProviderID})
				}
				body = map[string]any{"resources": resources, "meta": map[string]any{"total": 8}}
			case strings.HasPrefix(path, "/v2/droplets/"):
				id := strings.TrimPrefix(path, "/v2/droplets/")
				name := ""
				for _, n := range b.ProviderNodes {
					if n.ProviderID == id {
						name = n.Name
					}
				}
				if name == "" {
					t.Errorf("unexpected droplet ID %s", id)
					return nil, ErrProviderEffectSource
				}
				numeric, _ := strconv.ParseUint(id, 10, 64)
				body = map[string]any{"droplet": map[string]any{"id": numeric, "name": name, "status": "active", "vpc_uuid": WorkVPCUUID, "tags": []string{"norn-work-staging"}, "privateField": "not retained", "networks": map[string]any{"v4": []any{map[string]any{"type": "private", "ip_address": "10.43.0.13"}, map[string]any{"type": "public", "ip_address": "159.203.100.16"}}}}}
			default:
				t.Errorf("unexpected provider path %s", path)
				return nil, ErrProviderEffectSource
			}
			if mutate != nil {
				mutate(path, body)
			}
			encoded, _ := json.Marshal(body)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{}, Request: req}, nil
		})}
		return s, &calls
	}
	s, calls := fixtures(nil)
	raw, err := s.Observe(context.Background(), b)
	if err != nil || *calls != 11 {
		t.Fatalf("exact provider read: %v, calls=%d", err, *calls)
	}
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "privateField") || strings.Contains(string(raw), "extraSecret") {
		t.Fatal("transcript leaked redacted material")
	}
	var transcript providerEffectTranscript
	if err = json.Unmarshal(raw, &transcript); err != nil || len(transcript.Nodes) != 8 || len(transcript.Reads) != 11 {
		t.Fatalf("bad bounded transcript: %v %+v", err, transcript)
	}
	wrongAccount := func(path string, body map[string]any) {
		if path == "/v2/account" {
			body["account"].(map[string]any)["uuid"] = "personal"
		}
	}
	missingMember := func(path string, body map[string]any) {
		if strings.Contains(path, "/resources?") {
			body["resources"].([]any)[2].(map[string]any)["urn"] = "do:droplet:999"
		}
	}
	wrongIdentity := func(path string, body map[string]any) {
		if strings.HasSuffix(path, "/"+WorkDropletID) {
			body["droplet"].(map[string]any)["name"] = "app-04"
		}
	}
	wrongVPC := func(path string, body map[string]any) {
		if strings.HasSuffix(path, "/"+WorkDropletID) {
			body["droplet"].(map[string]any)["vpc_uuid"] = "other"
		}
	}
	wrongTag := func(path string, body map[string]any) {
		if strings.HasSuffix(path, "/"+WorkDropletID) {
			body["droplet"].(map[string]any)["tags"] = []string{"unrelated"}
		}
	}
	wrongIP := func(path string, body map[string]any) {
		if strings.HasSuffix(path, "/"+WorkDropletID) {
			v4 := body["droplet"].(map[string]any)["networks"].(map[string]any)["v4"].([]any)
			v4[0].(map[string]any)["ip_address"] = "10.43.0.99"
		}
	}
	for name, mutate := range map[string]func(string, map[string]any){"wrong account": wrongAccount, "missing member": missingMember, "wrong identity": wrongIdentity, "wrong VPC": wrongVPC, "wrong tag": wrongTag, "wrong IP": wrongIP} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixtures(mutate)
			if _, err := s.Observe(context.Background(), b); err == nil {
				t.Fatal("changed provider topology accepted")
			}
		})
	}
}
