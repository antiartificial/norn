package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"norn/v2/api/fleet/offline"
)

func terminalProviderBinding(t *testing.T) offline.Binding {
	t.Helper()
	_, binding, _, _ := offlineEffectFixture(t)
	binding.ProviderAccountUUID = offlineWorkAccountUUID
	return binding
}

func terminalProviderSource(t *testing.T, mutate func(*http.Request, int, *any)) (offlineDOAbsenceSource, *int) {
	t.Helper()
	reads := 0
	client := &http.Client{Transport: providerHTTPFixture(func(req *http.Request) (*http.Response, error) {
		reads++
		var body any
		status := http.StatusOK
		switch {
		case req.URL.Path == "/v2/account":
			body = map[string]any{"account": map[string]any{"uuid": offlineWorkAccountUUID}}
		case req.URL.Path == "/v2/projects/work-project/resources":
			body = map[string]any{"resources": []any{map[string]any{"urn": "do:droplet:101"}, map[string]any{"urn": "do:droplet:102"}}, "meta": map[string]any{"total": 2}}
		case req.URL.Path == "/v2/droplets":
			body = map[string]any{"droplets": []any{
				map[string]any{"id": 101, "name": "app-01", "status": "active", "vpc_uuid": "work-vpc", "tags": []string{"norn-work-staging"}},
				map[string]any{"id": 102, "name": "app-02", "status": "active", "vpc_uuid": "work-vpc", "tags": []string{"norn-work-staging"}},
			}, "meta": map[string]any{"total": 2}}
		case strings.HasPrefix(req.URL.Path, "/v2/droplets/"):
			status = http.StatusNotFound
			body = map[string]any{"id": "not_found", "message": offlineDONotFoundMessage}
		default:
			t.Fatalf("unexpected provider path %s", req.URL.RequestURI())
		}
		if mutate != nil {
			mutate(req, reads, &body)
		}
		raw, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}
	source := offlineDOAbsenceSource{
		reader:   offlineDOReader{client: client, token: func(context.Context) (string, error) { return "private-read-token", nil }, projectID: "work-project"},
		topology: offlineProviderTopology{AccountUUID: offlineWorkAccountUUID, ProjectID: "work-project", VPCUUID: "work-vpc", RequiredTags: []string{"norn-work-staging"}},
		clock:    func() time.Time { return time.Unix(100, 0).UTC() },
	}
	return source, &reads
}

func TestOfflineDOAbsenceSourceProducesPrivateExactEvidence(t *testing.T) {
	source, reads := terminalProviderSource(t, nil)
	observation, err := source.ObserveExactWorkNodeAbsent(context.Background(), terminalProviderBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	if observation.ObservedAt.IsZero() || len(observation.RawEvidence) == 0 || len(observation.EvidenceSHA256) != 64 {
		t.Fatalf("incomplete private evidence: %+v", observation)
	}
	if *reads != 9 {
		t.Fatalf("expected two complete inventories plus exact lookup, got %d reads", *reads)
	}
	if !strings.Contains(string(observation.RawEvidence), `"id":"not_found"`) || !strings.Contains(string(observation.RawEvidence), `"nodeName":"app-03"`) {
		t.Fatalf("raw evidence omitted exact absence: %s", observation.RawEvidence)
	}
}

func TestOfflineDOAbsenceSourceRejectsBad404ReplacementAndUnstableInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*http.Request, int, *any)
	}{
		{name: "wrong 404 envelope", mutate: func(req *http.Request, _ int, body *any) {
			if strings.HasPrefix(req.URL.Path, "/v2/droplets/") {
				*body = map[string]any{"id": "not_found", "message": "missing"}
			}
		}},
		{name: "replacement app-03", mutate: func(req *http.Request, _ int, body *any) {
			if req.URL.Path == "/v2/projects/work-project/resources" {
				value := (*body).(map[string]any)
				value["resources"] = append(value["resources"].([]any), map[string]any{"urn": "do:droplet:103"})
				value["meta"] = map[string]any{"total": 3}
			} else if req.URL.Path == "/v2/droplets" {
				value := (*body).(map[string]any)
				value["droplets"] = append(value["droplets"].([]any), map[string]any{"id": 103, "name": "app-03", "status": "active", "vpc_uuid": "work-vpc", "tags": []string{"norn-work-staging"}})
				value["meta"] = map[string]any{"total": 3}
			}
		}},
		{name: "tagless replacement app-03", mutate: func(req *http.Request, _ int, body *any) {
			if req.URL.Path == "/v2/droplets" {
				value := (*body).(map[string]any)
				value["droplets"] = append(value["droplets"].([]any), map[string]any{"id": 103, "name": "app-03", "status": "new", "vpc_uuid": "work-vpc", "tags": []string{}})
				value["meta"] = map[string]any{"total": 3}
			}
		}},
		{name: "wrong-vpc replacement app-03", mutate: func(req *http.Request, _ int, body *any) {
			if req.URL.Path == "/v2/droplets" {
				value := (*body).(map[string]any)
				value["droplets"] = append(value["droplets"].([]any), map[string]any{"id": 103, "name": "app-03", "status": "new", "vpc_uuid": "other-vpc", "tags": []string{"norn-work-staging"}})
				value["meta"] = map[string]any{"total": 3}
			}
		}},
		{name: "default-project replacement app-03", mutate: func(req *http.Request, _ int, body *any) {
			if req.URL.Path == "/v2/droplets" {
				value := (*body).(map[string]any)
				value["droplets"] = append(value["droplets"].([]any), map[string]any{"id": 103, "name": "app-03", "status": "new", "vpc_uuid": "work-vpc", "tags": []string{"norn-work-staging"}})
				value["meta"] = map[string]any{"total": 3}
			}
		}},
		{name: "inventory changed", mutate: func(req *http.Request, read int, body *any) {
			if req.URL.Path == "/v2/droplets" && read > 5 {
				value := (*body).(map[string]any)
				value["droplets"].([]any)[0].(map[string]any)["status"] = "off"
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _ := terminalProviderSource(t, tc.mutate)
			if _, err := source.ObserveExactWorkNodeAbsent(context.Background(), terminalProviderBinding(t)); !errors.Is(err, offline.ErrTerminalProof) {
				t.Fatalf("unsafe absence accepted: %v", err)
			}
		})
	}
}

func TestNewOfflineDOAbsenceSourceDisablesProxyAndRedirects(t *testing.T) {
	source := newOfflineDOAbsenceSource(func(context.Context) (string, error) { return "token", nil }, "project", "vpc", []string{"tag"})
	transport, ok := source.reader.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("provider absence client permits a proxy")
	}
	if source.reader.client.CheckRedirect == nil || source.reader.client.Timeout <= 0 {
		t.Fatal("provider absence client is not bounded against redirects/time")
	}
}
