package etcdstore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"norn/v2/api/fleet/offline"
)

type providerFixture struct {
	account  string
	droplets []offlineProviderDroplet
	err      error
}

func (f providerFixture) AccountUUID(context.Context) (string, error) { return f.account, f.err }
func (f providerFixture) Droplets(context.Context) ([]offlineProviderDroplet, error) {
	return f.droplets, f.err
}

func providerRows(b offline.Binding) []offlineProviderDroplet {
	rows := make([]offlineProviderDroplet, 0, len(b.ProviderNodes))
	for _, n := range b.ProviderNodes {
		rows = append(rows, offlineProviderDroplet{ID: n.ProviderID, Name: n.Name, Status: "active", ProjectID: "work-project", VPCUUID: "work-vpc", Tags: []string{"norn-work-staging"}})
	}
	return rows
}

func TestProviderCheckedEffectReobserver(t *testing.T) {
	_, b, a, now := offlineEffectFixture(t)
	b.ProviderAccountUUID = offlineWorkAccountUUID
	rows := providerRows(b)
	observer := providerCheckedEffectReobserver{remaining: offlineTestReobserver{observation: offlineEffectObservation(b, a, now)}, provider: providerFixture{account: b.ProviderAccountUUID, droplets: rows}, topology: offlineProviderTopology{AccountUUID: b.ProviderAccountUUID, ProjectID: "work-project", VPCUUID: "work-vpc", RequiredTags: []string{"norn-work-staging"}}}
	got, err := observer.reobserveEffect(context.Background(), b, a)
	if err != nil || !got.ProviderExact {
		t.Fatalf("exact topology rejected: %+v %v", got, err)
	}
	mutations := map[string]func([]offlineProviderDroplet) []offlineProviderDroplet{
		"missing app-03": func(v []offlineProviderDroplet) []offlineProviderDroplet { return append(v[:2:2], v[3:]...) },
		"wrong vpc":      func(v []offlineProviderDroplet) []offlineProviderDroplet { v[2].VPCUUID = "other"; return v },
		"wrong project":  func(v []offlineProviderDroplet) []offlineProviderDroplet { v[2].ProjectID = "other"; return v },
		"missing tag":    func(v []offlineProviderDroplet) []offlineProviderDroplet { v[2].Tags = nil; return v },
		"duplicate":      func(v []offlineProviderDroplet) []offlineProviderDroplet { return append(v, v[2]) },
		"extra target node": func(v []offlineProviderDroplet) []offlineProviderDroplet {
			return append(v, offlineProviderDroplet{ID: "999", Name: "app-04", ProjectID: "work-project", VPCUUID: "work-vpc", Tags: []string{"norn-work-staging"}})
		},
		"renamed app-03": func(v []offlineProviderDroplet) []offlineProviderDroplet { v[2].Name = "app-04"; return v },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copyRows := append([]offlineProviderDroplet(nil), rows...)
			observer.provider = providerFixture{account: b.ProviderAccountUUID, droplets: mutate(copyRows)}
			if _, err := observer.reobserveEffect(context.Background(), b, a); !errors.Is(err, offline.ErrEffectProofMissing) {
				t.Fatalf("accepted ambiguous topology: %v", err)
			}
		})
	}
	observer.provider = providerFixture{account: "personal", droplets: rows}
	if _, err := observer.reobserveEffect(context.Background(), b, a); !errors.Is(err, offline.ErrEffectProofMissing) {
		t.Fatalf("wrong account: %v", err)
	}
}

type providerHTTPFixture func(*http.Request) (*http.Response, error)

func (f providerHTTPFixture) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfflineDOReaderPaginationAndReadOnly(t *testing.T) {
	seen := []string{}
	reader := offlineDOReader{client: &http.Client{Transport: providerHTTPFixture(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "api.digitalocean.com" || req.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
		}
		seen = append(seen, req.URL.RequestURI())
		var body any
		switch req.URL.RequestURI() {
		case "/v2/account":
			body = map[string]any{"account": map[string]any{"uuid": "work"}}
		case "/v2/projects/project/resources?per_page=200&page=1":
			body = map[string]any{"resources": []any{map[string]any{"urn": "do:droplet:101"}}, "meta": map[string]any{"total": 2}}
		case "/v2/projects/project/resources?per_page=200&page=2":
			body = map[string]any{"resources": []any{map[string]any{"urn": "do:droplet:102"}}, "meta": map[string]any{"total": 2}}
		case "/v2/droplets?per_page=200&page=1":
			body = map[string]any{"droplets": []any{map[string]any{"id": 101, "name": "app-01", "status": "active", "vpc_uuid": "vpc", "tags": []string{"work"}}}, "meta": map[string]any{"total": 2}}
		case "/v2/droplets?per_page=200&page=2":
			body = map[string]any{"droplets": []any{map[string]any{"id": 102, "name": "app-02", "status": "active", "vpc_uuid": "vpc", "tags": []string{"work"}}}, "meta": map[string]any{"total": 2}}
		default:
			t.Fatalf("unexpected path: %s", req.URL.RequestURI())
		}
		encoded, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(encoded))), Header: http.Header{}}, nil
	})}, projectID: "project", token: func(context.Context) (string, error) { return "token", nil }}
	account, err := reader.AccountUUID(context.Background())
	if err != nil || account != "work" {
		t.Fatalf("account: %s %v", account, err)
	}
	rows, err := reader.Droplets(context.Background())
	if err != nil || len(rows) != 2 || rows[1].ProjectID != "project" {
		t.Fatalf("inventory: %+v %v", rows, err)
	}
	if len(seen) != 5 {
		t.Fatalf("pagination skipped: %v", seen)
	}
}
